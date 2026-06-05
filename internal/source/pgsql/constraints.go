package pgsql

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// Foreign keys
// ---------------------------------------------------------------------------

func (s *PgSQLSource) fetchTableForeignKeys(ctx context.Context, conn *pgxpool.Conn, tableOID uint32, tableName string) ([]*catalog.ForeignKey, error) {
	rows, err := conn.Query(ctx, `
		SELECT con.conname,
		       con.confupdtype::text,
		       con.confdeltype::text,
		       con.confmatchtype::text,
		       con.condeferrable,
		       con.condeferred,
		       refn.nspname || '.' || refc.relname AS foreign_table,
		       array_agg(att.attname ORDER BY u.ord) AS fk_cols,
		       array_agg(refa.attname ORDER BY u.ord) AS ref_cols
		FROM pg_catalog.pg_constraint con
		CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS u(fk_attnum, ref_attnum, ord)
		JOIN pg_catalog.pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = u.fk_attnum
		JOIN pg_catalog.pg_attribute refa ON refa.attrelid = con.confrelid AND refa.attnum = u.ref_attnum
		JOIN pg_catalog.pg_class refc ON refc.oid = con.confrelid
		JOIN pg_catalog.pg_namespace refn ON refn.oid = refc.relnamespace
		WHERE con.conrelid = $1 AND con.contype = 'f'
		GROUP BY con.conname, con.confupdtype::text, con.confdeltype::text, con.confmatchtype::text,
		         con.condeferrable, con.condeferred, refn.nspname, refc.relname
		ORDER BY con.conname
	`, tableOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ruleMap := map[string]string{
		"a": "NO ACTION",
		"r": "RESTRICT",
		"c": "CASCADE",
		"n": "SET NULL",
		"d": "SET DEFAULT",
	}
	matchMap := map[string]string{
		"s": "SIMPLE",
		"f": "FULL",
		"p": "PARTIAL",
	}

	var fkeys []*catalog.ForeignKey
	for rows.Next() {
		var name, updRule, delRule, matchRule string
		var deferrable, deferred bool
		var foreignTable string
		var fkCols, refCols []string
		if err := rows.Scan(&name, &updRule, &delRule, &matchRule,
			&deferrable, &deferred, &foreignTable, &fkCols, &refCols); err != nil {
			return nil, err
		}

		fk := &catalog.ForeignKey{
			Name:              name,
			TableName:         tableName,
			Columns:           fkCols,
			ForeignTable:      foreignTable,
			ForeignColumns:    refCols,
			UpdateRule:        ruleMap[updRule],
			DeleteRule:        ruleMap[delRule],
			MatchRule:         matchMap[matchRule],
			Deferrable:        deferrable,
			InitiallyDeferred: deferred,
		}
		fkeys = append(fkeys, fk)
	}
	return fkeys, rows.Err()
}

// ---------------------------------------------------------------------------
// Triggers
// ---------------------------------------------------------------------------

func (s *PgSQLSource) fetchTableTriggers(ctx context.Context, conn *pgxpool.Conn, tableOID uint32) ([]*catalog.Trigger, error) {
	rows, err := conn.Query(ctx, `
		SELECT tgname,
		       CASE tgenabled::text
		       WHEN 'O' THEN 'ENABLED'
		       WHEN 'D' THEN 'DISABLED'
		       ELSE 'UNKNOWN'
		       END,
		       pg_catalog.pg_get_triggerdef(t.oid) AS trigger_def
		FROM pg_catalog.pg_trigger t
		WHERE t.tgrelid = $1 AND NOT t.tgisinternal
		ORDER BY tgname
	`, tableOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var triggers []*catalog.Trigger
	for rows.Next() {
		var name, status, def string
		if err := rows.Scan(&name, &status, &def); err != nil {
			return nil, err
		}

		timing, events := parseTriggerDef(def)

		triggers = append(triggers, &catalog.Trigger{
			Name:   name,
			Timing: timing,
			Events: events,
			Action: def,
		})
	}
	return triggers, rows.Err()
}

// parseTriggerDef extracts timing and events from pg_get_triggerdef output.
func parseTriggerDef(def string) (timing string, events string) {
	parts := strings.Fields(def)
	for i, p := range parts {
		upper := strings.ToUpper(p)
		var eventStart int
		if upper == "BEFORE" || upper == "AFTER" {
			timing = upper
			eventStart = i + 1
		} else if upper == "INSTEAD" && i+1 < len(parts) && strings.ToUpper(parts[i+1]) == "OF" {
			timing = "INSTEAD OF"
			eventStart = i + 2
		} else {
			continue
		}
		var evts []string
		for j := eventStart; j < len(parts); j++ {
			u := strings.ToUpper(parts[j])
			if u == "ON" {
				break
			}
			if u != "OR" {
				evts = append(evts, u)
			}
		}
		events = strings.Join(evts, ", ")
		break
	}
	return timing, events
}
