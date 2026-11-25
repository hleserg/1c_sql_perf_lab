package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// RepairAccess maps an existing login in one database. Extra db_owner
// membership requires the explicit role argument.
func RepairAccess(ctx context.Context, p Profile, login, role string) (string, error) {
	l := &Live{P: p}
	defer l.Close()
	if err := l.connect(ctx, "master"); err != nil {
		return "", err
	}
	return repairAccessOnConn(ctx, l.conn, p, login, role)
}

func repairAccessOnConn(ctx context.Context, c *sql.Conn, p Profile, login, role string) (string, error) {
	if strings.TrimSpace(login) == "" || len(utf16.Encode([]rune(login))) > 128 || strings.ContainsRune(login, 0) {
		return "", errors.New("задай существующий логин SQL в -sql-login")
	}
	if role != "" && role != "connect" && role != "db_owner" {
		return "", errors.New("допустимые значения -access-role: connect и db_owner")
	}
	if err := checkMaster(ctx, c, p); err != nil {
		return "", err
	}
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// The restore workflow uses precisely this master-scoped application lock.
	var lock int
	if err = tx.QueryRowContext(ctx, `DECLARE @r int,@key nvarchar(255)=N'1c_sql_perf_lab:'+UPPER(@p1);
 EXEC @r=master.sys.sp_getapplock @Resource=@key,@LockMode='Exclusive',@LockOwner='Transaction',@LockTimeout=0;
 SELECT @r`, p.Database).Scan(&lock); err != nil {
		return "", err
	}
	if lock < 0 {
		return "", errors.New("на стендовой базе уже выполняется операция Perf Lab")
	}
	var db string
	err = tx.QueryRowContext(ctx, `SELECT name FROM sys.databases d WHERE name=@p1 AND NOT (`+protectedDatabase+`)
 AND state_desc='ONLINE' AND user_access_desc='MULTI_USER' AND is_read_only=0`, p.Database).Scan(&db)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("нужна доступная для записи пользовательская база стенда ONLINE/MULTI_USER вне репликации, снимков и групп доступности")
	}
	if err != nil {
		return "", err
	}

	var name, principalType string
	var sid []byte
	var systemAccount bool
	err = tx.QueryRowContext(ctx, `SELECT name,sid,type,CONVERT(bit,CASE WHEN sid IN
 (0x010100000000000512000000,0x010100000000000513000000,0x010100000000000514000000) THEN 1 ELSE 0 END)
 FROM sys.server_principals sp WHERE name=@p1 AND type IN ('S','U') AND is_disabled=0
 AND NOT EXISTS(SELECT 1 FROM sys.server_permissions perm WHERE perm.class=100 AND perm.grantee_principal_id=sp.principal_id AND perm.permission_name=N'CONNECT SQL' AND perm.state='D')`, login).Scan(&name, &sid, &principalType, &systemAccount)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("логин %q должен уже существовать на сервере, быть видимым и включённым, без запрета CONNECT SQL", login)
	}
	if err != nil {
		return "", err
	}

	// Execute the metadata check and DDL in the selected database. All names
	// supplied to DDL come from sys.* and are quoted with QUOTENAME.
	const mapping = `SET NOCOUNT ON;
 IF EXISTS(SELECT 1 FROM sys.database_principals WHERE name=@login AND (sid IS NULL OR sid<>@sid OR type<>@type OR authentication_type NOT IN (1,3)))
   THROW 50001,N'Имя занято другим пользователем или SID отличается. Привязка и прежние права сохранены.',1;
 DECLARE @user sysname,@id int,@action nvarchar(256)=N'Привязка и право CONNECT уже существуют',@stmt nvarchar(max);
 SELECT @user=name,@id=principal_id FROM sys.database_principals WHERE sid=@sid AND type=@type AND (authentication_type IN (1,3) OR principal_id=1);
 IF EXISTS(SELECT 1 FROM sys.database_permissions WHERE class=0 AND grantee_principal_id=@id AND permission_name='CONNECT' AND state='D')
   THROW 50002,N'Для пользователя явно задан DENY CONNECT. Решение о снятии запрета должен принять администратор.',1;
 IF (@canOpen=1 AND @wantOwner=0) OR @id=1 OR IS_SRVROLEMEMBER(N'sysadmin',@login)=1
   SET @action=N'Доступ уже есть согласно проверке или роли владельца/sysadmin';
 ELSE
 BEGIN
   IF @user IS NULL
   BEGIN
     SET @stmt=N'CREATE USER '+QUOTENAME(@login)+N' FOR LOGIN '+QUOTENAME(@login);
     EXEC sys.sp_executesql @stmt;
     SET @user=@login;
     SET @action=N'Создан пользователь базы для указанного логина';
   END;
   SELECT @id=principal_id FROM sys.database_principals WHERE name=@user AND sid=@sid;
   IF @id IS NULL OR @id<=4 THROW 50003,N'Прямая привязка указанного логина не подтверждена.',1;
   IF NOT EXISTS(SELECT 1 FROM sys.database_permissions WHERE class=0 AND grantee_principal_id=@id AND permission_name='CONNECT' AND state IN ('G','W'))
   BEGIN
     SET @stmt=N'GRANT CONNECT TO '+QUOTENAME(@user);
     EXEC sys.sp_executesql @stmt;
     SET @action=N'Подтверждена привязка пользователя; выдано CONNECT';
   END;
   IF NOT EXISTS(SELECT 1 FROM sys.database_permissions WHERE class=0 AND grantee_principal_id=@id AND permission_name='CONNECT' AND state IN ('G','W'))
     THROW 50004,N'Проверка CONNECT в выбранной базе не прошла.',1;
   IF @wantOwner=1
   BEGIN
     IF NOT EXISTS (SELECT 1 FROM sys.database_role_members WHERE role_principal_id=DATABASE_PRINCIPAL_ID(N'db_owner') AND member_principal_id=@id)
     BEGIN
       SET @stmt=N'ALTER ROLE [db_owner] ADD MEMBER '+QUOTENAME(@user);
       EXEC sys.sp_executesql @stmt;
     END;
     IF NOT EXISTS (SELECT 1 FROM sys.database_role_members WHERE role_principal_id=DATABASE_PRINCIPAL_ID(N'db_owner') AND member_principal_id=@id)
       THROW 50005,N'Не подтверждено членство в роли db_owner выбранной базы.',1;
     SET @action=N'Проверены SID, CONNECT и членство в роли db_owner этой базы';
   END;
 END;
 SELECT @action;`
	impersonate := func() (bool, error) {
		var allowed sql.NullInt64
		err := tx.QueryRowContext(ctx, `DECLARE @access int; EXECUTE AS LOGIN=@p1;
 BEGIN TRY SET @access=HAS_DBACCESS(@p2); END TRY
 BEGIN CATCH REVERT; THROW; END CATCH;
 REVERT; SELECT @access`, name, db).Scan(&allowed)
		return allowed.Valid && allowed.Int64 == 1, err
	}
	canOpen := false
	if !systemAccount {
		if canOpen, err = impersonate(); err != nil {
			return "", fmt.Errorf("проверить вход логина %q: %s", name, accessError(err))
		}
	}
	var action string
	err = tx.QueryRowContext(ctx, "EXEC "+quotedID(db)+".sys.sp_executesql "+unicodeLiteral(mapping)+", N'@login sysname,@sid varbinary(85),@type char(1),@canOpen bit,@wantOwner bit',@p1,@p2,@p3,@p4,@p5", name, sid, principalType, canOpen, role == "db_owner").Scan(&action)
	if err != nil {
		return "", errors.New(accessError(err))
	}
	verification := "Подтверждено HAS_DBACCESS под указанным логином."
	if systemAccount {
		verification = "Проверены метаданные учётной записи Windows. Проверить фактический вход LocalSystem/LocalService/NetworkService можно повторным входом из 1С."
	} else if ok, err := impersonate(); err != nil {
		return "", fmt.Errorf("проверить доступ после привязки: %s", accessError(err))
	} else if !ok {
		return "", errors.New("указанный логин всё ещё не может открыть базу. Изменения отменены")
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s / %s · %s. %s. %s", p.ExpectedServer, db, name, action, verification), nil
}
