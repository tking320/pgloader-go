package cast

// PgDefaultRules returns the default PostgreSQL-to-PostgreSQL cast rules.
// Most PG types map 1:1 to themselves; only special types need explicit rules.
func PgDefaultRules() []CastRule {
	return []CastRule{
		// money → numeric (strip currency formatting)
		{Match: MatchRule{SourceType: "money"}, TargetType: "numeric", DropTypemod: true, Transform: "money-to-numeric"},

		// txid_snapshot → text
		{Match: MatchRule{SourceType: "txid_snapshot"}, TargetType: "text", DropTypemod: true},

		// pg_lsn → text (log sequence number)
		{Match: MatchRule{SourceType: "pg_lsn"}, TargetType: "text", DropTypemod: true},

		// oid family → oid (keep as-is)
		{Match: MatchRule{SourceType: "oid"}, TargetType: "oid", DropTypemod: true},
		{Match: MatchRule{SourceType: "regclass"}, TargetType: "regclass", DropTypemod: true},
		{Match: MatchRule{SourceType: "regproc"}, TargetType: "regproc", DropTypemod: true},
		{Match: MatchRule{SourceType: "regprocedure"}, TargetType: "regprocedure", DropTypemod: true},
		{Match: MatchRule{SourceType: "regoper"}, TargetType: "regoper", DropTypemod: true},
		{Match: MatchRule{SourceType: "regoperator"}, TargetType: "regoperator", DropTypemod: true},
		{Match: MatchRule{SourceType: "regclass"}, TargetType: "regclass", DropTypemod: true},
		{Match: MatchRule{SourceType: "regtype"}, TargetType: "regtype", DropTypemod: true},
		{Match: MatchRule{SourceType: "regrole"}, TargetType: "regrole", DropTypemod: true},
		{Match: MatchRule{SourceType: "regnamespace"}, TargetType: "regnamespace", DropTypemod: true},
		{Match: MatchRule{SourceType: "regconfig"}, TargetType: "regconfig", DropTypemod: true},
		{Match: MatchRule{SourceType: "regdictionary"}, TargetType: "regdictionary", DropTypemod: true},

		// xid → bigint (xid is a 32-bit transaction ID, bigint for safety)
		{Match: MatchRule{SourceType: "xid"}, TargetType: "bigint", DropTypemod: true},
		{Match: MatchRule{SourceType: "cid"}, TargetType: "bigint", DropTypemod: true},

		// tid → tid (pass through)
		{Match: MatchRule{SourceType: "tid"}, TargetType: "tid", DropTypemod: true},

		// pg_node_tree → text (internal representation)
		{Match: MatchRule{SourceType: "pg_node_tree"}, TargetType: "text", DropTypemod: true},

		// aclitem → text
		{Match: MatchRule{SourceType: "aclitem"}, TargetType: "text", DropTypemod: true},

		// Standard types pass through 1:1 — handled by the default fallback
		// in Engine.Apply() which lowercases the type name.
	}
}
