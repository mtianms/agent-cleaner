package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testCleaner(t *testing.T) (*cleaner, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	npmPrefix := filepath.Join(home, "npm-global")
	t.Setenv("NPM_CONFIG_PREFIX", npmPrefix)
	t.Setenv("BUN_INSTALL", filepath.Join(home, ".bun"))
	service, err := newCleaner()
	if err != nil {
		t.Fatal(err)
	}
	return service, home
}

func TestResolvedPathContainmentRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if isResolvedPathInside(filepath.Join(link, "future.txt"), root) {
		t.Fatal("path through an outside symlink passed containment")
	}
	if !isResolvedPathInside(filepath.Join(root, "future", "file"), root) {
		t.Fatal("a missing path inside the profile was rejected")
	}
}

func TestCustomPackageRootsAreAllowedForAllowlistedPackages(t *testing.T) {
	service, _ := testCleaner(t)
	npmPrefix := filepath.Join(t.TempDir(), "npm-prefix")
	bunInstall := filepath.Join(t.TempDir(), "bun-install")
	t.Setenv("NPM_CONFIG_PREFIX", npmPrefix)
	t.Setenv("BUN_INSTALL", bunInstall)

	npmRoot := filepath.Join(npmPrefix, "node_modules")
	if runtime.GOOS != "windows" {
		npmRoot = filepath.Join(npmPrefix, "lib", "node_modules")
	}
	fixtures := []struct {
		id, root, name string
	}{
		{"pi-cli", npmRoot, "@earendil-works/pi-coding-agent"},
		{"omp-pi-bun", filepath.Join(bunInstall, "install", "global", "node_modules"), "@oh-my-pi/pi-coding-agent"},
	}
	for _, fixture := range fixtures {
		path := packagePath(fixture.root, fixture.name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "package.json"), []byte(`{"version":"1.0.0"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}

	found := service.scan()
	for _, fixture := range fixtures {
		var candidate item
		for _, scanned := range found.Items {
			if scanned.ID == fixture.id {
				candidate = scanned
				break
			}
		}
		if candidate.ID == "" {
			t.Fatalf("package %q was not detected", fixture.id)
		}
		if service.inUserRoots(candidate.Targets[0].Path) {
			t.Fatalf("fixture %q unexpectedly falls inside the temporary profile", fixture.id)
		}
		if err := service.validateCandidate(candidate); err != nil {
			t.Fatalf("custom package root %q was rejected: %v", candidate.Targets[0].Path, err)
		}
	}
}

func TestScanFindsAllowlistedPackagesAndFixedPaths(t *testing.T) {
	service, home := testCleaner(t)
	npmPrefix := filepath.Join(home, "npm-global")
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codex, "config.toml"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(service.local, "omp", "omp.exe")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, packageFixture := range []struct {
		name, version string
	}{
		{"@earendil-works/pi-coding-agent", "1.4.0"},
		{"@mariozechner/pi-coding-agent", "0.73.1"},
	} {
		npmRoot := filepath.Join(npmPrefix, "node_modules")
		if runtime.GOOS != "windows" {
			npmRoot = filepath.Join(npmPrefix, "lib", "node_modules")
		}
		path := packagePath(npmRoot, packageFixture.name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(map[string]string{"version": packageFixture.version})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "package.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	bunPath := packagePath(filepath.Join(home, ".bun", "install", "global", "node_modules"), "@oh-my-pi/pi-coding-agent")
	if err := os.MkdirAll(bunPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bunPath, "package.json"), []byte(`{"version":"18.12.6"}`), 0600); err != nil {
		t.Fatal(err)
	}

	found := service.scan()
	byID := make(map[string]item, len(found.Items))
	for _, candidate := range found.Items {
		byID[candidate.ID] = candidate
	}
	codexItem, ok := byID["codex-data"]
	if !ok || len(codexItem.Targets) != 1 || codexItem.Targets[0].Path != codex {
		t.Fatalf("codex target = %#v, want path %q", codexItem, codex)
	}
	binaryItem, ok := byID["omp-pi-binary"]
	if !ok || len(binaryItem.Targets) != 1 || binaryItem.Targets[0].Path != binary || binaryItem.Targets[0].Bytes != 7 {
		t.Fatalf("OMP binary target = %#v", binaryItem)
	}
	if byID["pi-cli"].Version != "1.4.0" || byID["pi-cli-legacy"].Version != "0.73.1" {
		t.Fatalf("npm package versions were not detected: %#v %#v", byID["pi-cli"], byID["pi-cli-legacy"])
	}
	if byID["omp-pi-bun"].Version != "18.12.6" {
		t.Fatalf("Bun package version = %q", byID["omp-pi-bun"].Version)
	}
	if !strings.Contains(found.QuarantineRoot, ".agent-cleaner-quarantine") {
		t.Fatalf("quarantine root = %q", found.QuarantineRoot)
	}
}

func TestSelectionConfirmationAndPathBoundaries(t *testing.T) {
	allowed := map[string]item{"codex-data": {ID: "codex-data"}, "cursor-data": {ID: "cursor-data"}}
	selected, err := normalizeSelection([]string{"codex-data"}, allowed)
	if err != nil || len(selected) != 1 || selected[0] != "codex-data" {
		t.Fatalf("normalizeSelection = %v, %v", selected, err)
	}
	for _, ids := range [][]string{nil, {}, {"missing"}, {"codex-data", "codex-data"}} {
		if _, err := normalizeSelection(ids, allowed); err == nil {
			t.Fatalf("normalizeSelection(%v) unexpectedly succeeded", ids)
		}
	}
	if err := validateRemovalMode(modeQuarantine, "DELETE"); err != nil {
		t.Fatal(err)
	}
	if err := validateRemovalMode(modePermanent, "DELETE PERMANENTLY"); err != nil {
		t.Fatal(err)
	}
	for _, confirmation := range []string{"DELETE", "delete permanently", " DELETE PERMANENTLY"} {
		if err := validateRemovalMode(modePermanent, confirmation); err == nil {
			t.Fatalf("permanent mode accepted %q", confirmation)
		}
	}

	root := filepath.Join(t.TempDir(), "profile")
	if !isPathInside(filepath.Join(root, ".codex", "config.toml"), root) {
		t.Fatal("path inside profile was rejected")
	}
	if isPathInside(root+"-backup", root) {
		t.Fatal("sibling path passed the containment check")
	}
}

func TestQuarantineRestoreCollisionAndPermanentRemoval(t *testing.T) {
	service, home := testCleaner(t)
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(codex, "config.toml")
	if err := os.WriteFile(dataPath, []byte("keep this local data"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := service.remove([]string{"codex-data"}, modePermanent, "DELETE"); err == nil {
		t.Fatal("wrong confirmation was accepted")
	}
	if _, err := os.Stat(dataPath); err != nil {
		t.Fatalf("wrong confirmation removed data: %v", err)
	}

	outcome, err := service.remove([]string{"codex-data"}, modeQuarantine, "DELETE")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.QuarantineID == "" || !sessionIDPattern.MatchString(outcome.QuarantineID) || !outcome.Results[0].OK {
		t.Fatalf("quarantine outcome = %#v", outcome)
	}
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatalf("source folder still exists after quarantine: %v", err)
	}

	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := service.restore(outcome.QuarantineID)
	if len(blocked) != 1 || blocked[0].OK || !strings.Contains(blocked[0].Message, "will not be overwritten") {
		t.Fatalf("restore collision result = %#v", blocked)
	}
	contents, err := os.ReadFile(dataPath)
	if err != nil || string(contents) != "do not overwrite" {
		t.Fatalf("restore overwrote existing data: %q, %v", contents, err)
	}
	if err := os.RemoveAll(codex); err != nil {
		t.Fatal(err)
	}

	restored := service.restore(outcome.QuarantineID)
	if len(restored) != 1 || !restored[0].OK {
		t.Fatalf("restore result = %#v", restored)
	}
	contents, err = os.ReadFile(dataPath)
	if err != nil || string(contents) != "keep this local data" {
		t.Fatalf("restored data = %q, %v", contents, err)
	}
	if sessions := service.scan().Sessions; len(sessions) != 0 {
		t.Fatalf("restored session remains available: %#v", sessions)
	}

	continueData := filepath.Join(home, ".continue")
	if err := os.MkdirAll(continueData, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(continueData, "data.json"), []byte("temporary"), 0600); err != nil {
		t.Fatal(err)
	}
	permanent, err := service.remove([]string{"continue-data"}, modePermanent, "DELETE PERMANENTLY")
	if err != nil {
		t.Fatal(err)
	}
	if !permanent.Results[0].OK {
		t.Fatalf("permanent removal result = %#v", permanent.Results[0])
	}
	if _, err := os.Stat(continueData); !os.IsNotExist(err) {
		t.Fatalf("permanent removal left data behind: %v", err)
	}
}

func TestRestoreRejectsManifestPathsOutsideProfile(t *testing.T) {
	service, _ := testCleaner(t)
	sessionID := "20260101000000-deadbeef"
	sessionDir := filepath.Join(service.quarantineRoot, sessionID)
	externalDir := t.TempDir()
	externalFile := filepath.Join(externalDir, "private.txt")
	quarantinedFile := filepath.Join(sessionDir, "items", "private.txt")
	if err := os.MkdirAll(filepath.Dir(quarantinedFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(externalFile, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantinedFile, []byte("quarantined"), 0600); err != nil {
		t.Fatal(err)
	}
	stored := manifest{
		ID: sessionID, CreatedAt: "2026-01-01T00:00:00Z",
		Items: []manifestItem{{ID: "codex-data-1", ItemName: "ignored", Source: externalFile, Destination: quarantinedFile}},
	}
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	results := service.restore(sessionID)
	if len(results) != 1 || results[0].OK || !strings.Contains(results[0].Message, "outside user data locations") {
		t.Fatalf("unsafe restore result = %#v", results)
	}
	if _, err := os.Stat(externalFile); err != nil {
		t.Fatalf("external path was altered: %v", err)
	}
	if _, err := os.Stat(quarantinedFile); err != nil {
		t.Fatalf("quarantined data was lost: %v", err)
	}
	if service.inUserRoots(externalFile) {
		t.Fatal("external fixture unexpectedly falls inside user data roots")
	}
}
