package cron

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// RegisterTaskFatal aborts the whole server when a task has no
// "admin.dashboard.<task>" translation — a missing i18n key is therefore a full
// outage, not a cosmetic gap. v1.26.27 shipped without
// admin.dashboard.fail_unsatisfiable_jobs and git.hanzo.ai served 503 until it
// was rolled back. This keeps that from shipping again.
func TestEveryRegisteredCronTaskHasATranslation(t *testing.T) {
	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, "options", "locale", "locale_en-US.json"))
	if err != nil {
		t.Fatalf("read locale: %v", err)
	}
	var locale map[string]any
	if err := json.Unmarshal(raw, &locale); err != nil {
		t.Fatalf("parse locale: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(".", "tasks*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	re := regexp.MustCompile(`RegisterTaskFatal\("([^"]+)"`)
	var checked int
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			task := m[1]
			checked++
			if _, ok := locale["admin.dashboard."+task]; !ok {
				t.Errorf("%s registers cron task %q with no locale key "+
					"admin.dashboard.%s — RegisterTaskFatal will abort startup",
					filepath.Base(f), task, task)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no RegisterTaskFatal calls — the regex or layout changed")
	}
	t.Logf("verified %d registered cron tasks have translations", checked)
}
