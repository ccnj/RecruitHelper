package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMessageSourceKeyAutoMigrationAndScopedNullableUniqueIndex(t *testing.T) {
	dir := t.TempDir()
	legacyDB, err := gorm.Open(sqlite.Open("file:"+filepath.Join(dir, "brain.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.AutoMigrate(&messageBeforeRetraction{}); err != nil {
		t.Fatal(err)
	}
	legacy := messageBeforeRetraction{
		Platform: "zhilian", AccountRef: "legacy-account", ConversationRef: "legacy-conversation", Seq: 1,
		Direction: "in", Kind: "text", ContentHash: "legacy-hash", Origin: "external",
	}
	if err := legacyDB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	legacySQL, err := legacyDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("升级无 source_key 的旧 messages 表: %v", err)
	}
	defer s.Close()

	var migrated Message
	if err := s.db.First(&migrated,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		legacy.Platform, legacy.AccountRef, legacy.ConversationRef, legacy.Seq).Error; err != nil {
		t.Fatal(err)
	}
	if migrated.SourceKey != nil {
		t.Fatalf("旧消息必须迁移为真 NULL，不得伪造等值键: %q", *migrated.SourceKey)
	}

	type indexListRow struct {
		Name   string
		Unique int
	}
	var indexes []indexListRow
	if err := s.db.Raw("PRAGMA index_list('messages')").Scan(&indexes).Error; err != nil {
		t.Fatal(err)
	}
	foundUnique := false
	for _, index := range indexes {
		if index.Name == "idx_messages_source_key" {
			foundUnique = index.Unique == 1
		}
	}
	if !foundUnique {
		t.Fatalf("缺少 sourceKey 作用域唯一索引: %+v", indexes)
	}
	type indexColumn struct {
		Seq  int    `gorm:"column:seqno"`
		Name string `gorm:"column:name"`
	}
	var columns []indexColumn
	if err := s.db.Raw("PRAGMA index_info('idx_messages_source_key')").Order("seqno").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, column := range columns {
		names = append(names, column.Name)
	}
	wantNames := []string{"platform", "account_ref", "conversation_ref", "source_key"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("sourceKey 唯一索引作用域=%v, want %v", names, wantNames)
	}

	key := strings.Repeat("d", 64)
	rows := []Message{
		{Platform: "zhilian", AccountRef: "account", ConversationRef: "conversation", Seq: 1,
			Direction: "in", Kind: "text", ContentHash: "null-1", Origin: "external"},
		{Platform: "zhilian", AccountRef: "account", ConversationRef: "conversation", Seq: 2,
			Direction: "in", Kind: "text", ContentHash: "null-2", Origin: "external"},
		{Platform: "zhilian", AccountRef: "account", ConversationRef: "conversation", Seq: 3,
			Direction: "in", Kind: "text", ContentHash: "keyed", Origin: "external", SourceKey: &key},
	}
	if err := s.db.Create(&rows).Error; err != nil {
		t.Fatalf("nullable sourceKey 应允许同 scope 多条 NULL: %v", err)
	}
	duplicate := Message{
		Platform: "zhilian", AccountRef: "account", ConversationRef: "conversation", Seq: 4,
		Direction: "in", Kind: "text", ContentHash: "duplicate", Origin: "external", SourceKey: &key,
	}
	if err := s.db.Create(&duplicate).Error; err == nil {
		t.Fatal("同 platform+account+conversation 的 sourceKey 必须唯一")
	}
	otherScopes := []Message{
		{Platform: "zhilian", AccountRef: "account", ConversationRef: "other-conversation", Seq: 1,
			Direction: "in", Kind: "text", ContentHash: "other-conversation", Origin: "external", SourceKey: &key},
		{Platform: "zhilian", AccountRef: "other-account", ConversationRef: "conversation", Seq: 1,
			Direction: "in", Kind: "text", ContentHash: "other-account", Origin: "external", SourceKey: &key},
		{Platform: "other", AccountRef: "account", ConversationRef: "conversation", Seq: 1,
			Direction: "in", Kind: "text", ContentHash: "other-platform", Origin: "external", SourceKey: &key},
	}
	if err := s.db.Create(&otherScopes).Error; err != nil {
		t.Fatalf("相同 sourceKey 在其他作用域应允许: %v", err)
	}
}

func TestMessageSourceKeyValidationAndJSONBoundary(t *testing.T) {
	valid := strings.Repeat("e", 64)
	if err := validateMessageDrafts([]MessageDraft{{
		Direction: "in", Kind: "text", ContentHash: "hash", Origin: "external", SourceKey: &valid,
	}}); err != nil {
		t.Fatalf("有效 sourceKey 不应被 store 拒绝: %v", err)
	}
	for _, invalid := range []string{"", strings.Repeat("e", 63), strings.Repeat("E", 64), strings.Repeat("z", 64)} {
		err := validateMessageDrafts([]MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: "hash", Origin: "external", SourceKey: &invalid,
		}})
		if !errors.Is(err, ErrInvalidMessageSourceKey) {
			t.Fatalf("非法 sourceKey 必须被 store 拒绝: len=%d err=%v", len(invalid), err)
		}
	}

	raw, err := json.Marshal(Message{SourceKey: &valid})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), valid) || strings.Contains(string(raw), "SourceKey") || strings.Contains(string(raw), "sourceKey") {
		t.Fatalf("Message 直接序列化不得向管理端/UI 泄露 sourceKey: %s", raw)
	}
}
