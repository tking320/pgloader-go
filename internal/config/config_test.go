package config

import (
	"testing"
)

func TestApplyWithOption_Comments默认关闭(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Comments {
		t.Error("expected Comments=false by default")
	}
}

func TestApplyWithOption_Comments启用(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.ApplyWithOption("comments"); err != nil {
		t.Fatalf("ApplyWithOption('comments') failed: %v", err)
	}
	if !cfg.Comments {
		t.Error("expected Comments=true after 'comments' option")
	}
}

func TestApplyWithOption_NoComments关闭(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Comments = true
	if err := cfg.ApplyWithOption("no comments"); err != nil {
		t.Fatalf("ApplyWithOption('no comments') failed: %v", err)
	}
	if cfg.Comments {
		t.Error("expected Comments=false after 'no comments' option")
	}
}

func TestApplyWithOption_Comments不影响其他选项(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CreateTables = true
	cfg.Truncate = true

	if err := cfg.ApplyWithOption("comments"); err != nil {
		t.Fatalf("ApplyWithOption('comments') failed: %v", err)
	}

	if !cfg.Comments {
		t.Error("expected Comments=true")
	}
	if !cfg.CreateTables {
		t.Error("expected CreateTables unchanged")
	}
	if !cfg.Truncate {
		t.Error("expected Truncate unchanged")
	}
}
