// Package workspace 实现「一次性工作区 + diff 审批」。
//
// 做法：不把真实项目目录直接读写挂进容器，而是先复制出一份临时副本挂进去；
// agent 的所有改动只落在副本里。容器退出后计算副本与原目录的 diff 给用户看，
// 用户批准后才把改动回写真实目录。这样「只有你认可的改动才会离开沙箱」。
package workspace

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

// Snapshot 表示一次一次性工作区。
type Snapshot struct {
	Src string // 原始项目目录
	Dir string // 临时副本（挂载进容器的那个）
}

// Prepare 把 src 完整复制到一个临时目录，返回快照。
func Prepare(src string) (*Snapshot, error) {
	dir, err := os.MkdirTemp("", "sbx-ws-")
	if err != nil {
		return nil, err
	}
	if err := copyTree(src, dir); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("复制工作区失败: %w", err)
	}
	return &Snapshot{Src: src, Dir: dir}, nil
}

// Diff 返回副本相对原目录的差异文本，以及是否有改动。
func (s *Snapshot) Diff() (string, bool, error) {
	// diff 以退出码 1 表示“有差异”，这不是错误，所以忽略退出码。
	out, _ := exec.Command("diff", "-ruN", "--exclude=.git", s.Src, s.Dir).CombinedOutput()
	changed := len(bytes.TrimSpace(out)) > 0
	return string(out), changed, nil
}

// Apply 把副本的状态镜像回原目录（含新增/修改/删除）。
func (s *Snapshot) Apply() error {
	if _, err := exec.LookPath("rsync"); err == nil {
		return run("rsync", "-a", "--delete", withSlash(s.Dir), withSlash(s.Src))
	}
	// 退路：无 rsync 时用 cp（不处理删除）。
	return run("cp", "-a", s.Dir+"/.", s.Src)
}

// Cleanup 删除临时副本。
func (s *Snapshot) Cleanup() error {
	if s.Dir == "" {
		return nil
	}
	return os.RemoveAll(s.Dir)
}

func copyTree(src, dst string) error {
	if _, err := exec.LookPath("rsync"); err == nil {
		return run("rsync", "-a", withSlash(src), withSlash(dst))
	}
	return run("cp", "-a", src+"/.", dst)
}

func run(name string, args ...string) error {
	c := exec.Command(name, args...)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%s 失败: %v: %s", name, err, out)
	}
	return nil
}

func withSlash(p string) string {
	if len(p) > 0 && p[len(p)-1] == '/' {
		return p
	}
	return p + "/"
}
