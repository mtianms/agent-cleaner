package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const sizeScanLimit = 8000

type itemKind string

const (
	kindNPM       itemKind = "npm"
	kindBun       itemKind = "bun"
	kindFile      itemKind = "file"
	kindDirectory itemKind = "directory"
)

type target struct {
	Path            string `json:"path"`
	Type            string `json:"type"`
	Bytes           int64  `json:"bytes"`
	SizeApproximate bool   `json:"sizeApproximate"`
}

type item struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Kind        itemKind `json:"kind"`
	Description string   `json:"description"`
	Warning     string   `json:"warning"`
	PackageName string   `json:"packageName,omitempty"`
	Version     string   `json:"version,omitempty"`
	Targets     []target `json:"targets"`
}

type catalog struct {
	ScannedAt      string        `json:"scannedAt"`
	Items          []item        `json:"items"`
	Sessions       []sessionInfo `json:"sessions"`
	QuarantineRoot string        `json:"quarantineRoot"`
}

type packageDefinition struct {
	id, packageName, name, group, description, warning string
}

type pathDefinition struct {
	id, path, name, group, description, warning string
}

type cleaner struct {
	home, local, appData, quarantineRoot string
}

var npmPackages = []packageDefinition{
	{"codex-cli", "@openai/codex", "OpenAI Codex CLI", "agents", "CLI agent installed globally through npm.", "npm package removal is permanent. Codex configuration and data are listed separately."},
	{"opencode-cli", "@opencode/cli", "OpenCode CLI", "agents", "CLI agent installed globally through npm.", "npm package removal is permanent. OpenCode configuration is listed separately."},
	{"caveman-code", "@juliusbrussee/caveman-code", "Caveman Code", "agents", "Coding agent installed globally through npm.", "npm package removal is permanent."},
	{"9router", "9router", "9Router", "support", "A local model router, not an agent. Included as a detected support tool.", "Select only if you no longer use this router."},
	{"pi-cli", "@earendil-works/pi-coding-agent", "Pi Coding Agent", "agents", "Pi CLI agent installed globally through npm.", "npm package removal is permanent. Pi data is listed separately."},
	{"pi-cli-legacy", "@mariozechner/pi-coding-agent", "Pi Coding Agent (legacy package)", "agents", "A legacy Pi package that may still be installed globally through npm.", "npm package removal is permanent. Pi data is listed separately."},
}

var bunPackages = []packageDefinition{
	{"omp-pi-bun", "@oh-my-pi/pi-coding-agent", "OMP Pi CLI", "agents", "OMP Pi coding agent installed globally through Bun.", "Bun package removal is permanent. OMP data is listed separately."},
}

func newCleaner() (*cleaner, error) {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}

	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = filepath.Join(home, "AppData", "Local")
	}
	local, err = filepath.Abs(local)
	if err != nil {
		return nil, err
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	appData, err = filepath.Abs(appData)
	if err != nil {
		return nil, err
	}

	return &cleaner{
		home:           filepath.Clean(home),
		local:          filepath.Clean(local),
		appData:        filepath.Clean(appData),
		quarantineRoot: filepath.Join(filepath.Clean(home), ".agent-cleaner-quarantine"),
	}, nil
}

func (c *cleaner) scan() catalog {
	items := make([]item, 0)
	if root := c.discoverNPMRoot(); root != "" {
		for _, definition := range npmPackages {
			path := packagePath(root, definition.packageName)
			version, ok := readPackageVersion(filepath.Join(path, "package.json"))
			if !ok {
				continue
			}
			items = append(items, packageItem(definition, kindNPM, path, version, "npm package"))
		}
	}

	bunRoot := c.discoverBunRoot()
	for _, definition := range bunPackages {
		path := packagePath(bunRoot, definition.packageName)
		version, ok := readPackageVersion(filepath.Join(path, "package.json"))
		if !ok {
			continue
		}
		items = append(items, packageItem(definition, kindBun, path, version, "Bun global package"))
	}

	for _, definition := range c.fileDefinitions() {
		if target, ok := makeTarget(definition.path); ok {
			items = append(items, pathItem(definition, kindFile, target))
		}
	}
	for _, definition := range c.directoryDefinitions() {
		if target, ok := makeTarget(definition.path); ok {
			items = append(items, pathItem(definition, kindDirectory, target))
		}
	}

	grokTargets := make([]target, 0, 2)
	for _, path := range []string{
		filepath.Join(c.home, ".grok", "hooks"),
		filepath.Join(c.home, ".grok", "managed_config.lock"),
	} {
		if found, ok := makeTarget(path); ok {
			grokTargets = append(grokTargets, found)
		}
	}
	if len(grokTargets) > 0 {
		items = append(items, item{
			ID:          "grok-data",
			Name:        "Additional Grok data",
			Group:       "remnants",
			Kind:        kindDirectory,
			Description: "Remaining hooks and managed_config.lock files in the Grok folder.",
			Warning:     "The detected hooks folder contains Orca integration status files. Select this only if you also want to remove that integration.",
			Targets:     grokTargets,
		})
	}

	groupOrder := map[string]int{"agents": 0, "remnants": 1, "runtime": 2, "support": 3}
	sort.Slice(items, func(i, j int) bool {
		if groupOrder[items[i].Group] != groupOrder[items[j].Group] {
			return groupOrder[items[i].Group] < groupOrder[items[j].Group]
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})

	return catalog{
		ScannedAt:      time.Now().Format(time.RFC3339),
		Items:          items,
		Sessions:       c.listQuarantine(),
		QuarantineRoot: c.quarantineRoot,
	}
}

func (c *cleaner) discoverNPMRoot() string {
	if prefix := os.Getenv("NPM_CONFIG_PREFIX"); prefix != "" {
		if runtime.GOOS == "windows" {
			return filepath.Join(prefix, "node_modules")
		}
		return filepath.Join(prefix, "lib", "node_modules")
	}
	if _, err := osExecutable("npm"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := runPackageManager(ctx, "npm", []string{"root", "--global"})
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

func (c *cleaner) discoverBunRoot() string {
	bunInstall := os.Getenv("BUN_INSTALL")
	if bunInstall == "" {
		bunInstall = filepath.Join(c.home, ".bun")
	}
	return filepath.Join(bunInstall, "install", "global", "node_modules")
}

func (c *cleaner) fileDefinitions() []pathDefinition {
	return []pathDefinition{
		{"grok-cli", filepath.Join(c.home, ".grok", "bin", "grok.exe"), "Grok CLI", "agents", "Standalone Grok CLI executable.", "This executable will be moved to quarantine or deleted, depending on the selected mode."},
		{"tiny-agents", filepath.Join(c.local, "Programs", "Python", "Python313", "Scripts", "tiny-agents.exe"), "Tiny Agents launcher", "agents", "Launcher found in the Python 3.13 Scripts folder.", "Only this detected launcher is affected. Installations in other Python environments are not included."},
		{"omp-pi-binary", filepath.Join(c.local, "omp", "omp.exe"), "OMP Pi CLI", "agents", "OMP Pi executable from the Windows binary installer.", "Close active OMP Pi sessions before moving or deleting this executable."},
	}
}

func (c *cleaner) directoryDefinitions() []pathDefinition {
	return []pathDefinition{
		{"codex-data", filepath.Join(c.home, ".codex"), "Codex configuration and data", "remnants", "Codex settings, authentication, plugins, history, sessions, and cache.", "May contain login tokens, prompt history, plugins, and work data. It can use significant disk space."},
		{"claude-data", filepath.Join(c.home, ".claude"), "Claude Code configuration", "remnants", "Remaining Claude Code configuration files.", "This folder may also contain custom skills or files."},
		{"claude-cache", filepath.Join(c.local, "claude-cli-nodejs"), "Claude Code cache", "remnants", "Local MCP cache and logs from previous Claude Code use.", "Logs may contain project names or other local information."},
		{"cursor-data", filepath.Join(c.home, ".cursor"), "Cursor configuration", "remnants", "Remaining Cursor settings, hooks, or skills.", "Review the contents. This folder may contain custom skills or hooks."},
		{"continue-data", filepath.Join(c.home, ".continue"), "Continue configuration", "remnants", "Continue configuration and local data.", "Local data may include sessions and model configuration."},
		{"opencode-data", filepath.Join(c.home, ".config", "opencode"), "OpenCode configuration", "remnants", "Remaining OpenCode skills, plugins, and preferences.", "This folder may contain custom skills or plugins."},
		{"codex-runtime", filepath.Join(c.local, "OpenAI", "Codex"), "Local Codex / OpenAI runtime", "runtime", "OpenAI binaries and local runtime detected in AppData.", "Other OpenAI apps may use this component. Removing it may require a new download or interfere with those apps."},
		{"pi-data", filepath.Join(c.home, ".pi", "agent"), "Pi configuration and data", "remnants", "Pi settings, credentials, sessions, extensions, skills, and packages.", "May contain API keys, sign-in data, session history, extensions, or custom skills."},
		{"omp-data", filepath.Join(c.home, ".omp"), "OMP Pi configuration and data", "remnants", "OMP Pi global settings, profiles, authentication, sessions, and cache.", "May contain provider credentials, session history, profiles, and custom configuration."},
	}
}

func packageItem(definition packageDefinition, kind itemKind, path, version, targetType string) item {
	return item{
		ID:          definition.id,
		Name:        definition.name,
		Group:       definition.group,
		Kind:        kind,
		Description: definition.description,
		Warning:     definition.warning,
		PackageName: definition.packageName,
		Version:     version,
		Targets:     []target{{Path: path, Type: targetType}},
	}
}

func pathItem(definition pathDefinition, kind itemKind, found target) item {
	return item{
		ID:          definition.id,
		Name:        definition.name,
		Group:       definition.group,
		Kind:        kind,
		Description: definition.description,
		Warning:     definition.warning,
		Targets:     []target{found},
	}
}

func packagePath(root, name string) string {
	return filepath.Join(append([]string{root}, strings.Split(name, "/")...)...)
}

func readPackageVersion(file string) (string, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return "", false
	}
	if manifest.Version == "" {
		manifest.Version = "unknown version"
	}
	return manifest.Version, true
}

type sizeBudget struct {
	visited int
	bytes   int64
	capped  bool
}

func measurePath(path string, budget *sizeBudget) {
	if budget.capped {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		budget.capped = true
		return
	}
	budget.visited++
	if budget.visited > sizeScanLimit {
		budget.capped = true
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return
	}
	if info.Mode().IsRegular() {
		budget.bytes += info.Size()
		return
	}
	if !info.IsDir() {
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		budget.capped = true
		return
	}
	for _, entry := range entries {
		if budget.capped {
			break
		}
		measurePath(filepath.Join(path, entry.Name()), budget)
	}
}

func makeTarget(path string) (target, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return target{}, false
	}
	budget := &sizeBudget{}
	measurePath(path, budget)
	typeName := "file"
	if info.IsDir() {
		typeName = "folder"
	}
	return target{Path: path, Type: typeName, Bytes: budget.bytes, SizeApproximate: budget.capped}, true
}

func (c *cleaner) userRoots() []string {
	return []string{c.home, c.local, c.appData}
}

func (c *cleaner) nameForID(id string) string {
	for _, definition := range npmPackages {
		if definition.id == id {
			return definition.name
		}
	}
	for _, definition := range bunPackages {
		if definition.id == id {
			return definition.name
		}
	}
	for _, definition := range c.fileDefinitions() {
		if definition.id == id {
			return definition.name
		}
	}
	for _, definition := range c.directoryDefinitions() {
		if definition.id == id {
			return definition.name
		}
	}
	if id == "grok-data" {
		return "Additional Grok data"
	}
	return ""
}
