package workbench

import (
	"errors"
	"strings"
	"testing"
)

func TestPackageInstallFailureClassification(t *testing.T) {
	const cmd = "APT_FAILED exit=100 command=apt-get install -y --no-install-recommends git curl ripgrep\n"
	for _, tc := range []struct {
		name, log string
		reason    string
		pkg       string
	}{
		{"dns", "Err:1 https://deb.debian.org/debian trixie InRelease\n  Temporary failure resolving 'deb.debian.org'\n" + cmd, "无法解析下载服务器域名", "git curl ripgrep"},
		{"dns-nodata", "E: Failed to fetch https://deb.debian.org/debian/pool/main/n/nodejs/nodejs.deb  Something wicked happened resolving 'deb.debian.org:https' (-5 - No address associated with hostname)\n" + cmd, "无法解析下载服务器域名", "git curl ripgrep"},
		{"timeout", "Could not connect to 10.0.2.2:7890 (10.0.2.2). - connect (110: Connection timed out)\n" + cmd, "连接下载服务器失败或超时", "git curl ripgrep"},
		{"missing", "E: Unable to locate package ripgrep-x\n" + cmd, "找不到所需的软件包", "ripgrep-x"},
		{"candidate", "E: Package 'fd-find' has no installation candidate\nnext line\n" + cmd, "找不到所需的软件包", "fd-find"},
		{"unknown", "something odd happened\n" + cmd, "没有可识别的原因", "git curl ripgrep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := packageInstallFailure("安装 Linux 基础软件包", errors.New("exit status 1"), "Installing base packages...\n"+tc.log)
			if err == nil {
				t.Fatal("expected failure")
			}
			for _, want := range []string{"安装 Linux 基础软件包失败", tc.reason, "失败命令：apt-get install", "涉及软件包：" + tc.pkg, "错误输出末尾"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "7890") || strings.Contains(err.Error(), "deb.debian.org") {
				t.Fatalf("unredacted address: %v", err)
			}
		})
	}
}

func TestPackageInstallFailureWithoutMarkerFallsBack(t *testing.T) {
	if err := packageInstallFailure("安装", errors.New("x"), "Installing base packages...\nboom"); err != nil {
		t.Fatalf("no evidence must keep the generic path: %v", err)
	}
}

func TestPackageInstallFailureBoundsAndRedacts(t *testing.T) {
	log := strings.Repeat("noise\n", 50) + "proxy=http://user:pass@127.0.0.1:7890 token=abc123 " + strings.Repeat("x", 500) + "\nAPT_FAILED exit=1 command=apt-get update\n"
	msg := packageInstallFailure("安装", errors.New("x"), log).Error()
	for _, bad := range []string{"user:pass", "127.0.0.1", "abc123"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("leaked %q: %s", bad, msg)
		}
	}
	if len(msg) > 1500 || strings.Count(msg, "noise") > aptTailLines {
		t.Fatalf("not bounded: %d", len(msg))
	}
}

func TestPackageInstallFailureReadsOlderSetupOutput(t *testing.T) {
	const output = "E: Unable to locate package missing-package\n"
	const payload = "exit=100 command=apt-get install missing-package\n"
	cause := errors.New("exit status 100")
	current := packageInstallFailure("安装", cause, output+"APT_FAILED "+payload)
	older := packageInstallFailure("安装", cause, output+"CAGE_APT_FAILED "+payload)
	if current == nil || older == nil || current.Error() != older.Error() {
		t.Fatalf("old setup output lost its failure details: current=%v older=%v", current, older)
	}
}
