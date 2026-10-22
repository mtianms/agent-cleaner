package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

type removalMode string

const (
	modeQuarantine removalMode = "quarantine"
	modePermanent  removalMode = "permanent"
)

type manifestItem struct {
	ID          string `json:"id"`
	ItemName    string `json:"itemName"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Restored    bool   `json:"restored"`
}

type manifest struct {
	ID        string         `json:"id"`
	CreatedAt string         `json:"createdAt"`
	Items     []manifestItem `json:"items"`
}

type sessionInfo struct {
	ID        string   `json:"id"`
	CreatedAt string   `json:"createdAt"`
	Count     int      `json:"count"`
	Names     []string `json:"names"`
}

type operationResult struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Message string `json:"message,omitempty"`
}

type removalOutcome struct {
	Mode         removalMode       `json:"mode"`
	Results      []operationResult `json:"results"`
	QuarantineID string            `json:"quarantineId,omitempty"`
}

type quarantineSession struct {
	id       string
	dir      string
	manifest manifest
}

var sessionIDPattern = regexp.MustCompile(`^[0-9]{14}-[a-f0-9]{8}$`)
var itemIndexSuffix = regexp.MustCompile(`-\d+$`)

func normalizeSelection(ids []string, allowed map[string]item) ([]string, error) {
	if len(ids) == 0 || len(ids) > len(allowed) {
		return nil, errors.New("Select at least one detected item.")
	}
	selected := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return nil, errors.New("Selection is invalid. Rescan and select an item from the list.")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errors.New("Selection is invalid. Rescan and select an item from the list.")
		}
		seen[id] = struct{}{}
		selected = append(selected, id)
	}
	return selected, nil
}

func validateRemovalMode(mode removalMode, confirmation string) error {
	if mode == modeQuarantine && confirmation == "DELETE" {
		return nil
	}
	if mode == modePermanent && confirmation == "DELETE PERMANENTLY" {
		return nil
	}
	return errors.New("Confirmation does not match the removal mode.")
}

func isPathInside(candidate, parent string) bool {
	child, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	child, root = filepath.Clean(child), filepath.Clean(root)
	if runtime.GOOS == "windows" {
		child, root = strings.ToLower(child), strings.ToLower(root)
	}
	relative, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func resolvedPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	missing := make([]string, 0)
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func isResolvedPathInside(candidate, parent string) bool {
	child, err := resolvedPath(candidate)
	if err != nil {
		return false
	}
	root, err := resolvedPath(parent)
	if err != nil {
		return false
	}
	return isPathInside(child, root)
}

func sameVolume(first, second string) bool {
	firstAbs, firstErr := filepath.Abs(first)
	secondAbs, secondErr := filepath.Abs(second)
	if firstErr != nil || secondErr != nil {
		return false
	}
	return strings.EqualFold(filepath.VolumeName(firstAbs), filepath.VolumeName(secondAbs))
}

func (c *cleaner) validateCandidate(candidate item) error {
	if candidate.Kind == kindNPM {
		if !packageAllowed(npmPackages, candidate.ID, candidate.PackageName) {
			return errors.New("npm package is not in the allowlist.")
		}
		for _, target := range candidate.Targets {
			if isResolvedPathInside(target.Path, c.quarantineRoot) {
				return errors.New("Quarantine paths cannot be processed again.")
			}
		}
		return nil
	}
	if candidate.Kind == kindBun {
		if !packageAllowed(bunPackages, candidate.ID, candidate.PackageName) {
			return errors.New("Bun package is not in the allowlist.")
		}
		for _, target := range candidate.Targets {
			if isResolvedPathInside(target.Path, c.quarantineRoot) {
				return errors.New("Quarantine paths cannot be processed again.")
			}
		}
		return nil
	}
	for _, target := range candidate.Targets {
		inside := false
		for _, root := range c.userRoots() {
			if isResolvedPathInside(target.Path, root) {
				inside = true
				break
			}
		}
		if !inside {
			return fmt.Errorf("Path is outside user data locations: %s", target.Path)
		}
		if isResolvedPathInside(target.Path, c.quarantineRoot) {
			return errors.New("Quarantine paths cannot be processed again.")
		}
	}
	return nil
}

func packageAllowed(definitions []packageDefinition, id, packageName string) bool {
	for _, definition := range definitions {
		if definition.id == id && definition.packageName == packageName {
			return true
		}
	}
	return false
}

func (c *cleaner) preview(ids []string) ([]item, error) {
	found := c.scan().Items
	byID := make(map[string]item, len(found))
	for _, candidate := range found {
		byID[candidate.ID] = candidate
	}
	selected, err := normalizeSelection(ids, byID)
	if err != nil {
		return nil, err
	}
	items := make([]item, 0, len(selected))
	for _, id := range selected {
		candidate := byID[id]
		if err := c.validateCandidate(candidate); err != nil {
			return nil, err
		}
		items = append(items, candidate)
	}
	return items, nil
}

func (c *cleaner) remove(ids []string, mode removalMode, confirmation string) (removalOutcome, error) {
	if err := validateRemovalMode(mode, confirmation); err != nil {
		return removalOutcome{}, err
	}
	found := c.scan().Items
	byID := make(map[string]item, len(found))
	for _, candidate := range found {
		byID[candidate.ID] = candidate
	}
	selected, err := normalizeSelection(ids, byID)
	if err != nil {
		return removalOutcome{}, err
	}
	selectedItems := make([]item, 0, len(selected))
	for _, id := range selected {
		candidate := byID[id]
		if err := c.validateCandidate(candidate); err != nil {
			return removalOutcome{}, err
		}
		selectedItems = append(selectedItems, candidate)
	}

	var session *quarantineSession
	if mode == modeQuarantine && hasFileTargets(selectedItems) {
		session, err = c.createSession()
		if err != nil {
			return removalOutcome{}, err
		}
	}

	results := make([]operationResult, 0, len(selectedItems))
	for _, selectedItem := range selectedItems {
		if selectedItem.Kind == kindNPM || selectedItem.Kind == kindBun {
			detail, removeErr := c.removePackage(selectedItem)
			results = append(results, operationResult{
				ID: selectedItem.ID, Name: selectedItem.Name, OK: removeErr == nil,
				Detail: firstNonEmpty(detail, "Package removed successfully."),
			})
			if removeErr != nil {
				results[len(results)-1].Detail = safeError(removeErr)
			}
			continue
		}

		var actions []operationResult
		if mode == modeQuarantine {
			actions = c.moveIntoQuarantine(selectedItem, session)
		} else {
			actions = removePermanently(selectedItem)
		}
		succeeded := len(actions) > 0
		allSucceeded := succeeded
		lines := make([]string, 0, len(actions))
		for _, action := range actions {
			if !action.OK {
				allSucceeded = false
			}
			state := "FAILED"
			if action.OK {
				state = "OK"
			}
			lines = append(lines, state+": "+action.Message)
		}
		results = append(results, operationResult{
			ID: selectedItem.ID, Name: selectedItem.Name, OK: succeeded && allSucceeded, Detail: strings.Join(lines, "\n"),
		})
	}

	outcome := removalOutcome{Mode: mode, Results: results}
	if session != nil {
		if len(session.manifest.Items) == 0 {
			_ = os.RemoveAll(session.dir)
		} else {
			outcome.QuarantineID = session.id
		}
	}
	return outcome, nil
}

func hasFileTargets(items []item) bool {
	for _, candidate := range items {
		if candidate.Kind != kindNPM && candidate.Kind != kindBun {
			return true
		}
	}
	return false
}

func removePermanently(candidate item) []operationResult {
	results := make([]operationResult, 0, len(candidate.Targets))
	for _, target := range candidate.Targets {
		path, err := filepath.Abs(target.Path)
		if err == nil {
			_, err = os.Lstat(path)
		}
		if err == nil {
			err = os.RemoveAll(path)
		}
		if err != nil {
			results = append(results, operationResult{OK: false, Message: path + ": " + safeError(err)})
		} else {
			results = append(results, operationResult{OK: true, Message: "Permanently deleted: " + path})
		}
	}
	return results
}

func (c *cleaner) removePackage(candidate item) (string, error) {
	switch candidate.Kind {
	case kindNPM:
		if !packageAllowed(npmPackages, candidate.ID, candidate.PackageName) {
			return "", errors.New("npm package does not match the allowlist.")
		}
		if _, err := osExecutable("npm"); err != nil {
			return "", errors.New("npm was not found in PATH.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		output, err := runPackageManager(ctx, "npm", []string{"uninstall", "--global", candidate.PackageName})
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(output)), nil
	case kindBun:
		if !packageAllowed(bunPackages, candidate.ID, candidate.PackageName) {
			return "", errors.New("Bun package does not match the allowlist.")
		}
		binary, ok := c.bunExecutable()
		if !ok {
			return "", errors.New("Bun was not found in the user installation or PATH.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		output, err := runExecutable(ctx, binary, []string{"remove", "--global", candidate.PackageName})
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(output)), nil
	default:
		return "", errors.New("Unsupported package manager.")
	}
}

func (c *cleaner) bunExecutable() (string, bool) {
	bunInstall := os.Getenv("BUN_INSTALL")
	if bunInstall == "" {
		bunInstall = filepath.Join(c.home, ".bun")
	}
	candidate := filepath.Join(bunInstall, "bin", "bun.exe")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, true
	}
	if os.Getenv("BUN_INSTALL") != "" {
		return "", false
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		candidate = filepath.Join(strings.Trim(directory, `"`), "bun.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func (c *cleaner) createSession() (*quarantineSession, error) {
	if !isResolvedPathInside(c.quarantineRoot, c.home) {
		return nil, errors.New("Quarantine folder must stay inside the user profile.")
	}
	if err := os.MkdirAll(c.quarantineRoot, 0700); err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("attrib.exe", "+h", c.quarantineRoot).Run()
	}
	for range 5 {
		id, err := newSessionID()
		if err != nil {
			return nil, err
		}
		directory := filepath.Join(c.quarantineRoot, id)
		if err := os.Mkdir(directory, 0700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return nil, err
		}
		session := &quarantineSession{
			id: id, dir: directory,
			manifest: manifest{ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Items: []manifestItem{}},
		}
		if err := writeManifest(directory, session.manifest); err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
		return session, nil
	}
	return nil, errors.New("Could not create a unique quarantine session.")
}

func newSessionID() (string, error) {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(random[:]), nil
}

func writeManifest(directory string, value manifest) error {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	temporaryPath := manifestPath + "." + hex.EncodeToString(random[:]) + ".tmp"
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	data, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr == nil {
		_, marshalErr = file.Write(append(data, '\n'))
	}
	closeErr := file.Close()
	if marshalErr != nil {
		_ = os.Remove(temporaryPath)
		return marshalErr
	}
	if closeErr != nil {
		_ = os.Remove(temporaryPath)
		return closeErr
	}
	if err := os.Rename(temporaryPath, manifestPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func (c *cleaner) moveIntoQuarantine(candidate item, session *quarantineSession) []operationResult {
	results := make([]operationResult, 0, len(candidate.Targets))
	for index, target := range candidate.Targets {
		source, err := filepath.Abs(target.Path)
		if err != nil {
			results = append(results, operationResult{OK: false, Message: target.Path + ": " + safeError(err)})
			continue
		}
		if !sameVolume(source, c.quarantineRoot) {
			results = append(results, operationResult{OK: false, Message: "Different volume, not moved: " + source})
			continue
		}
		if _, ok := makeTarget(source); !ok {
			results = append(results, operationResult{OK: false, Message: "Path no longer exists: " + source})
			continue
		}

		base := filepath.Base(source)
		if base == "." || base == string(filepath.Separator) || base == "" {
			base = fmt.Sprintf("item-%d", index+1)
		}
		destination := filepath.Join(session.dir, "items", fmt.Sprintf("%02d-%s", len(session.manifest.Items), base))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			results = append(results, operationResult{OK: false, Message: source + ": " + safeError(err)})
			continue
		}

		record := manifestItem{
			ID: fmt.Sprintf("%s-%d", candidate.ID, index+1), ItemName: candidate.Name,
			Source: source, Destination: destination,
		}
		session.manifest.Items = append(session.manifest.Items, record)
		if err := writeManifest(session.dir, session.manifest); err != nil {
			session.manifest.Items = session.manifest.Items[:len(session.manifest.Items)-1]
			results = append(results, operationResult{OK: false, Message: source + ": " + safeError(err)})
			continue
		}

		if err := os.Rename(source, destination); err != nil {
			sourceExists, sourceErr := pathExists(source)
			destinationExists, destinationErr := pathExists(destination)
			if sourceErr == nil && destinationErr == nil && destinationExists && !sourceExists {
				results = append(results, operationResult{OK: true, Message: "Moved to quarantine: " + source})
				continue
			}
			if sourceErr == nil && destinationErr == nil && sourceExists && !destinationExists {
				session.manifest.Items = session.manifest.Items[:len(session.manifest.Items)-1]
				_ = writeManifest(session.dir, session.manifest)
			}
			results = append(results, operationResult{OK: false, Message: source + ": " + safeError(err)})
			continue
		}
		results = append(results, operationResult{OK: true, Message: "Moved to quarantine: " + source})
	}
	return results
}

func (c *cleaner) listQuarantine() []sessionInfo {
	if !isResolvedPathInside(c.quarantineRoot, c.home) {
		return []sessionInfo{}
	}
	entries, err := os.ReadDir(c.quarantineRoot)
	if err != nil {
		return []sessionInfo{}
	}
	sessions := make([]sessionInfo, 0)
	for _, entry := range entries {
		if !entry.IsDir() || !sessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		directory := filepath.Join(c.quarantineRoot, entry.Name())
		if !isResolvedPathInside(directory, c.quarantineRoot) {
			continue
		}
		stored, ok := readManifest(directory, entry.Name())
		if !ok {
			continue
		}
		pending := make([]manifestItem, 0, len(stored.Items))
		for _, record := range stored.Items {
			if record.Restored {
				continue
			}
			source, sourceErr := filepath.Abs(record.Source)
			destination, destinationErr := filepath.Abs(record.Destination)
			if sourceErr != nil || destinationErr != nil || !c.inUserRoots(source) || !isPathInside(destination, directory) || !isResolvedPathInside(filepath.Dir(destination), directory) {
				pending = append(pending, record)
				continue
			}
			destinationExists, _ := pathExists(destination)
			sourceExists, _ := pathExists(source)
			if destinationExists || !sourceExists {
				pending = append(pending, record)
			}
		}
		if len(pending) == 0 {
			continue
		}
		names := make([]string, 0, len(pending))
		for _, record := range pending {
			name := c.nameForID(itemIndexSuffix.ReplaceAllString(record.ID, ""))
			if name == "" {
				name = record.ItemName
			}
			names = append(names, name)
		}
		sessions = append(sessions, sessionInfo{
			ID: entry.Name(), CreatedAt: stored.CreatedAt, Count: len(pending), Names: names,
		})
	}
	sortSessions(sessions)
	return sessions
}

func sortSessions(sessions []sessionInfo) {
	sort.Slice(sessions, func(i, j int) bool {
		first, firstErr := time.Parse(time.RFC3339Nano, sessions[i].CreatedAt)
		second, secondErr := time.Parse(time.RFC3339Nano, sessions[j].CreatedAt)
		if firstErr == nil && secondErr == nil {
			return first.After(second)
		}
		return sessions[i].CreatedAt > sessions[j].CreatedAt
	})
}

func (c *cleaner) restore(sessionID string) []operationResult {
	if !sessionIDPattern.MatchString(sessionID) {
		return []operationResult{{OK: false, Message: "Invalid quarantine session ID."}}
	}
	if !isResolvedPathInside(c.quarantineRoot, c.home) {
		return []operationResult{{OK: false, Message: "Quarantine folder is outside the user profile."}}
	}
	directory := filepath.Join(c.quarantineRoot, sessionID)
	if !isResolvedPathInside(directory, c.quarantineRoot) {
		return []operationResult{{OK: false, Message: "Quarantine session path is outside the quarantine folder."}}
	}
	stored, ok := readManifest(directory, sessionID)
	if !ok {
		return []operationResult{{OK: false, Message: "Quarantine manifest not found or invalid."}}
	}

	results := make([]operationResult, 0)
	for index := range stored.Items {
		record := &stored.Items[index]
		if record.Restored {
			continue
		}
		source, sourceErr := filepath.Abs(record.Source)
		destination, destinationErr := filepath.Abs(record.Destination)
		if sourceErr != nil || !c.inUserRoots(source) {
			results = append(results, operationResult{OK: false, Message: "Source path is outside user data locations: " + record.Source})
			continue
		}
		if destinationErr != nil || !isPathInside(destination, directory) || !isResolvedPathInside(filepath.Dir(destination), directory) {
			results = append(results, operationResult{OK: false, Message: "Quarantine path does not match the session."})
			continue
		}
		if _, err := os.Lstat(destination); err != nil {
			results = append(results, operationResult{OK: false, Message: destination + ": " + safeError(err)})
			continue
		}
		if _, err := os.Lstat(source); err == nil {
			results = append(results, operationResult{OK: false, Message: "Source path already exists and will not be overwritten: " + source})
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			results = append(results, operationResult{OK: false, Message: source + ": " + safeError(err)})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
			results = append(results, operationResult{OK: false, Message: source + ": " + safeError(err)})
			continue
		}
		if err := os.Rename(destination, source); err != nil {
			results = append(results, operationResult{OK: false, Message: destination + ": " + safeError(err)})
			continue
		}
		record.Restored = true
		if err := writeManifest(directory, stored); err != nil {
			results = append(results, operationResult{OK: false, Message: source + ": " + safeError(err)})
			continue
		}
		results = append(results, operationResult{OK: true, Message: "Restored: " + source})
	}
	return results
}

func (c *cleaner) inUserRoots(path string) bool {
	for _, root := range c.userRoots() {
		if isResolvedPathInside(path, root) {
			return true
		}
	}
	return false
}

func readManifest(directory, expectedID string) (manifest, bool) {
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return manifest{}, false
	}
	var stored manifest
	if json.Unmarshal(data, &stored) != nil || stored.ID != expectedID || stored.Items == nil {
		return manifest{}, false
	}
	return stored, true
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func osExecutable(name string) (string, error) {
	return exec.LookPath(name)
}

func runPackageManager(ctx context.Context, executable string, args []string) ([]byte, error) {
	if executable != "npm" {
		return nil, errors.New("Unsupported package manager.")
	}
	if runtime.GOOS == "windows" {
		command := strings.Join(append([]string{executable}, args...), " ")
		output, err := exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", command).CombinedOutput()
		if err != nil {
			return nil, commandError(output, err)
		}
		return output, nil
	}
	return runExecutable(ctx, executable, args)
}

func runExecutable(ctx context.Context, executable string, args []string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, executable, args...).CombinedOutput()
	if err != nil {
		return nil, commandError(output, err)
	}
	return output, nil
}

func commandError(output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return errors.New(safeError(errors.New(message)))
}

func safeError(err error) string {
	if err == nil {
		return "An unexpected error occurred."
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "An unexpected error occurred."
	}
	runes := []rune(message)
	if len(runes) > 1200 {
		message = string(runes[:1200])
	}
	return message
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
