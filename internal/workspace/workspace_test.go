package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotDiffAndApply(t *testing.T) {
	src := t.TempDir()
	write(t, src, "keep.txt", "unchanged")
	write(t, src, "edit.txt", "before")
	write(t, src, "gone.txt", "will be deleted by agent")

	snap, err := Prepare(src)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Cleanup()

	// 刚复制完：无差异。
	if _, changed, _ := snap.Diff(); changed {
		t.Fatal("刚快照不应有差异")
	}

	// 模拟 agent 在副本里的改动：改一个、加一个、删一个。
	write(t, snap.Dir, "edit.txt", "after")
	write(t, snap.Dir, "new.txt", "created by agent")
	os.Remove(filepath.Join(snap.Dir, "gone.txt"))

	diff, changed, _ := snap.Diff()
	if !changed {
		t.Fatal("应检测到差异")
	}
	if len(diff) == 0 {
		t.Fatal("diff 文本不应为空")
	}

	// 原目录此刻仍是旧状态（改动尚未离开沙箱）。
	if read(t, src, "edit.txt") != "before" {
		t.Fatal("审批前原目录不应被改动")
	}

	// 批准 → 回写。
	if err := snap.Apply(); err != nil {
		t.Fatal(err)
	}
	if read(t, src, "edit.txt") != "after" {
		t.Error("修改未回写")
	}
	if read(t, src, "new.txt") != "created by agent" {
		t.Error("新增未回写")
	}
	if _, err := os.Stat(filepath.Join(src, "gone.txt")); !os.IsNotExist(err) {
		t.Error("删除未同步（期望 gone.txt 被移除）")
	}
	if read(t, src, "keep.txt") != "unchanged" {
		t.Error("未改动文件不应丢失")
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}
