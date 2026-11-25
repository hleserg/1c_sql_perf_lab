package lab

import (
	"context"
	"errors"
)

// Limit the physical scan to partitions that have actually served user scans.
// These are stand trial bounds, not universal maintenance recommendations.
const usedIndexes = `SELECT TOP(12) p.object_id,p.index_id,p.partition_number,s.name AS schema_name,t.name AS table_name,i.name AS index_name,u.user_scans,p.used_page_count
 FROM sys.dm_db_partition_stats AS p JOIN sys.tables AS t ON t.object_id=p.object_id
 JOIN sys.schemas AS s ON s.schema_id=t.schema_id JOIN sys.indexes AS i ON i.object_id=p.object_id AND i.index_id=p.index_id
 JOIN sys.dm_db_index_usage_stats AS u ON u.database_id=DB_ID() AND u.object_id=i.object_id AND u.index_id=i.index_id
 WHERE t.is_ms_shipped=0 AND t.is_memory_optimized=0 AND i.type IN(1,2) AND i.is_disabled=0 AND i.is_hypothetical=0 AND i.allow_page_locks=1 AND u.user_scans>0 AND p.used_page_count BETWEEN 256 AND 32768
 ORDER BY u.user_scans DESC,p.used_page_count DESC,p.object_id,p.index_id,p.partition_number`

func (l *Live) maintainIndexes(ctx context.Context, save func(Section) error, note func(string)) error {
	const pick = `WITH used AS (` + usedIndexes + `) SELECT TOP(3) u.object_id AS [ID],u.index_id AS [Индекс ID],u.partition_number AS [Секция],u.schema_name AS [Схема],u.table_name AS [Таблица],u.index_name AS [Индекс],u.user_scans AS [Сканирований],f.page_count AS [Страниц],CONVERT(decimal(6,2),f.avg_fragmentation_in_percent) AS [Фрагментация %], CONVERT(decimal(6,2),f.avg_page_space_used_in_percent) AS [Плотность %]
 FROM used u CROSS APPLY sys.dm_db_index_physical_stats(DB_ID(),u.object_id,u.index_id,u.partition_number,'SAMPLED') f
 WHERE f.index_level=0 AND f.alloc_unit_type_desc='IN_ROW_DATA' AND f.avg_fragmentation_in_percent>=15 AND f.page_count BETWEEN 256 AND 32768 ORDER BY u.user_scans DESC,f.avg_fragmentation_in_percent DESC,u.object_id,u.index_id,u.partition_number`
	section := fetchSection(ctx, l.conn, check{32, "Используемые индексы перед REORGANIZE", pick})
	if err := save(section); err != nil {
		return err
	}
	if section.Error != "" {
		return errors.New(section.Error)
	}
	for _, row := range section.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		title := row[3] + "." + row[4] + " · " + row[5] + " · секция " + row[2]
		note(title)
		const apply = `DECLARE @obj int=CONVERT(int,@p1), @idx int=CONVERT(int,@p2), @part int=CONVERT(int,@p3), @cmd nvarchar(max);
 SELECT @cmd=N'ALTER INDEX '+QUOTENAME(i.name)+N' ON '+QUOTENAME(s.name)+N'.'+QUOTENAME(t.name)+N' REORGANIZE'+CASE WHEN ds.type='PS' THEN N' PARTITION = '+CONVERT(nvarchar(11),p.partition_number) ELSE N'' END+N' WITH (LOB_COMPACTION = OFF)'
 FROM sys.tables t JOIN sys.schemas s ON t.schema_id=s.schema_id JOIN sys.indexes i ON i.object_id=t.object_id
 JOIN sys.data_spaces ds ON ds.data_space_id=i.data_space_id JOIN sys.dm_db_partition_stats p ON p.object_id=i.object_id AND p.index_id=i.index_id
 WHERE t.object_id=@obj AND i.index_id=@idx AND p.partition_number=@part AND p.used_page_count BETWEEN 256 AND 32768 AND s.name=@p4 AND t.name=@p5 AND i.name=@p6 AND i.type IN (1,2) AND i.is_disabled=0 AND i.allow_page_locks=1 AND i.is_hypothetical=0 AND t.is_memory_optimized=0
 AND EXISTS(SELECT 1 FROM sys.dm_db_index_physical_stats(DB_ID(),@obj,@idx,@part,'LIMITED') f WHERE f.index_level=0 AND f.alloc_unit_type_desc='IN_ROW_DATA' AND f.avg_fragmentation_in_percent>=15);
 IF @cmd IS NOT NULL EXEC sys.sp_executesql @cmd;
 SELECT CASE WHEN @cmd IS NULL THEN N'Пропущено после перепроверки' ELSE N'Обслужено' END AS [Результат],f.page_count AS [Страниц после], CONVERT(decimal(6,2),f.avg_fragmentation_in_percent) AS [Фрагментация после %],CONVERT(decimal(6,2),f.avg_page_space_used_in_percent) AS [Плотность после %] FROM sys.dm_db_index_physical_stats(DB_ID(),@obj,@idx,@part,'SAMPLED') f WHERE index_level=0 AND alloc_unit_type_desc='IN_ROW_DATA'`
		s := fetchSection(ctx, l.conn, check{32, title, apply}, row[0], row[1], row[2], row[3], row[4], row[5])
		if err := save(s); err != nil {
			return err
		}
		if s.Error != "" {
			return errors.New(s.Error)
		}
	}
	return nil
}
