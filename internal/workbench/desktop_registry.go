package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
)

//go:embed desktop-inspection.py
var desktopInspection string

type desktopObservation struct {
	Generation int                  `json:"generation,omitempty"`
	Running    bool                 `json:"running"`
	Identity   *claudeGuestSnapshot `json:"identity"`
}

func validDesktopInstance(id string) bool { return id == "default" || validClaudeID(id) }

func (list claudeAccountList) validateDesktopBindings() error {
	used := map[string]bool{}
	if list.PendingDesktop != "" {
		if !validClaudeID(list.PendingDesktop) {
			return errors.New("待登录 Desktop 记录无效")
		}
		used[list.PendingDesktop] = true
	}
	for _, a := range list.Accounts {
		if id := a.DesktopInstanceID; id != "" {
			if used[id] || !validDesktopInstance(id) {
				return errors.New("多个账号引用了同一个 Desktop 或实例标识无效")
			}
			used[id] = true
		}
	}
	for id, target := range list.DesktopRedirects {
		if !validDesktopInstance(id) || !validDesktopInstance(target) || id == target || used[id] {
			return errors.New("Desktop 实例归属记录无效")
		}
	}
	for id, label := range list.DesktopLabels {
		if !validDesktopInstance(id) || strings.TrimSpace(label) != label || !validDesktopLabel(label) {
			return errors.New("Desktop 名称记录无效")
		}
	}
	if list.DefaultDesktopRemoved {
		if used["default"] || list.DesktopLabels["default"] != "" || list.DesktopRedirects["default"] != "" {
			return errors.New("已删除的默认 Desktop 记录无效")
		}
	}
	return nil
}

func (list *claudeAccountList) hasDefaultDesktop() bool {
	return !list.DefaultDesktopRemoved && list.DesktopRedirects["default"] == ""
}

// A record that cannot be read leaves the decision to the start job, which
// reports the read error itself.
func (s *Server) hasDefaultDesktop() bool {
	m := &s.claudeAccounts
	m.mu.Lock()
	list, err := s.loadClaudeAccounts()
	m.mu.Unlock()
	return err != nil || list.hasDefaultDesktop()
}

func (list *claudeAccountList) hasDesktop(id string) bool {
	if id == "default" {
		return list.hasDefaultDesktop()
	}
	return validClaudeID(id) && (list.desktopOwner(id) != nil || list.PendingDesktop == id)
}

func (list *claudeAccountList) desktopCount() int {
	count := 0
	if list.hasDefaultDesktop() {
		count++
	}
	for _, a := range list.Accounts {
		if a.DesktopInstanceID != "" && a.DesktopInstanceID != "default" {
			count++
		}
	}
	if list.PendingDesktop != "" {
		count++
	}
	return count
}

func (list *claudeAccountList) desktopOwner(id string) *claudeAccount {
	for i := range list.Accounts {
		if list.Accounts[i].DesktopInstanceID == id {
			return &list.Accounts[i]
		}
	}
	return nil
}

func (list *claudeAccountList) desktopEntry(id string) desktopEntry {
	var entry desktopEntry
	if a := list.desktopOwner(id); a != nil {
		entry = desktopEntry{ID: id, AccountID: a.ID, Number: a.Number, Label: a.Label, Dedicated: a.DesktopDedicatedNetwork || a.ProxyNodeID != ""}
	} else {
		label := "待登录 Desktop"
		if id == "default" {
			label = "默认 Desktop"
		}
		entry = desktopEntry{ID: id, Label: label}
	}
	if custom := list.DesktopLabels[id]; custom != "" {
		entry.Label = custom
	}
	return entry
}

func (list *claudeAccountList) renameDesktop(id, raw string) (desktopEntry, error) {
	label := strings.TrimSpace(raw)
	if !validDesktopLabel(label) {
		return desktopEntry{}, errors.New("Desktop 名称需要 1 至 100 个字符，不能含控制字符")
	}
	if !validDesktopInstance(id) || list.DesktopRedirects[id] != "" || !list.hasDesktop(id) {
		return desktopEntry{}, os.ErrNotExist
	}
	if list.DesktopLabels == nil {
		list.DesktopLabels = map[string]string{}
	}
	list.DesktopLabels[id] = label
	return list.desktopEntry(id), nil
}

func (list *claudeAccountList) deleteDesktop(id string) bool {
	if !list.hasDesktop(id) || list.desktopCount() <= 1 {
		return false
	}
	if id == "default" {
		list.DefaultDesktopRemoved = true
		if owner := list.desktopOwner("default"); owner != nil {
			owner.DesktopInstanceID = ""
			owner.DesktopStatus = ""
		}
		delete(list.DesktopLabels, "default")
		for old, target := range list.DesktopRedirects {
			if old == "default" || target == "default" {
				delete(list.DesktopRedirects, old)
			}
		}
		return true
	}
	if list.PendingDesktop == id {
		list.PendingDesktop = ""
	}
	if owner := list.desktopOwner(id); owner != nil {
		owner.DesktopInstanceID = ""
		owner.DesktopStatus = ""
	}
	if list.DesktopRedirects["default"] == id {
		list.DefaultDesktopRemoved = true
	}
	delete(list.DesktopLabels, id)
	for old, target := range list.DesktopRedirects {
		if old == id || target == id {
			delete(list.DesktopRedirects, old)
		}
	}
	return true
}

func (list *claudeAccountList) createDesktop(account string) (desktopEntry, error) {
	if account == "" {
		if list.PendingDesktop == "" {
			list.PendingDesktop = randomToken()[:24]
		}
		return list.desktopEntry(list.PendingDesktop), nil
	}
	a := list.find(account)
	if a == nil {
		return desktopEntry{}, errors.New("未找到所属账号")
	}
	if a.DesktopInstanceID == "" {
		id := a.ID
		if list.desktopOwner(id) != nil || list.DesktopRedirects[id] != "" || id == list.PendingDesktop {
			id = randomToken()[:24]
		}
		a.DesktopInstanceID = id
	}
	return list.desktopEntry(a.DesktopInstanceID), nil
}

func (s *Server) newAccountDesktop(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccountID string `json:"account_id"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !s.accountDesktopsSupported() {
		http.Error(w, "此环境尚未支持独立 Desktop", 501)
		return
	}
	s.op.Lock()
	defer s.op.Unlock()
	m := &s.claudeAccounts
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := s.loadClaudeAccounts()
	if err != nil {
		requestError(w, err)
		return
	}
	entry, err := list.createDesktop(input.AccountID)
	if err == nil {
		err = s.saveClaudeAccounts(list)
	}
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, entry)
}

func (s *Server) updateAccountDesktopMetadata(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validDesktopInstance(id) {
		http.NotFound(w, r)
		return
	}
	var input struct {
		Label *string `json:"label"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if input.Label == nil {
		http.Error(w, "请填写 Desktop 名称", http.StatusBadRequest)
		return
	}
	if !s.accountDesktopsSupported() {
		http.Error(w, "此环境尚未支持独立 Desktop", http.StatusNotImplemented)
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "另一个操作仍在进行，请稍候", http.StatusConflict)
		return
	}
	defer s.op.Unlock()
	m := &s.claudeAccounts
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := s.loadClaudeAccounts()
	if err != nil {
		requestError(w, err)
		return
	}
	entry, err := list.renameDesktop(id, *input.Label)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.saveClaudeAccounts(list); err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, entry)
}

func (s *Server) deleteAccountDesktop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validDesktopInstance(id) {
		http.NotFound(w, r)
		return
	}
	if !s.accountDesktopsSupported() {
		http.Error(w, "此环境尚未支持独立 Desktop", http.StatusNotImplemented)
		return
	}
	if !s.vmRunning() {
		http.Error(w, "请先启动环境", http.StatusConflict)
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "另一个操作仍在进行，请稍候", http.StatusConflict)
		return
	}
	defer s.op.Unlock()
	m := &s.claudeAccounts
	m.mu.Lock()
	list, err := s.loadClaudeAccounts()
	exists := err == nil && list.hasDesktop(id)
	remaining := 0
	if err == nil {
		remaining = list.desktopCount()
	}
	m.mu.Unlock()
	if err != nil {
		requestError(w, err)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	if remaining <= 1 {
		http.Error(w, "至少保留一个 Desktop 实例，不能删除最后一个", http.StatusBadRequest)
		return
	}
	if err := s.changeAccountDesktop(s.operationContext(), id, claudeAccount{}, "delete"); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	m.mu.Lock()
	list, err = s.loadClaudeAccounts()
	deleted := false
	if err == nil {
		deleted = list.deleteDesktop(id)
		if deleted {
			err = s.saveClaudeAccounts(list)
		}
	}
	m.mu.Unlock()
	if err != nil {
		requestError(w, err)
		return
	}
	if !deleted {
		http.Error(w, "至少保留一个 Desktop 实例，不能删除最后一个", http.StatusBadRequest)
		return
	}
	if id == "default" {
		s.desktopStatus.invalidate()
	}
	jsonReply(w, map[string]bool{"ok": true})
}

// Directory presence identifies a previously created Desktop. Unused account
// records alone do not create instances during upgrade.
func (list *claudeAccountList) migrateDesktopInstances(observed map[string]desktopObservation) bool {
	changed := false
	for i := range list.Accounts {
		a := &list.Accounts[i]
		_, exists := observed[a.ID]
		if exists && a.DesktopInstanceID == "" && list.desktopOwner(a.ID) == nil && a.ID != list.PendingDesktop && list.DesktopRedirects[a.ID] == "" {
			a.DesktopInstanceID = a.ID
			changed = true
		}
	}
	return changed
}

// Reconcile only a native client's completed identity observation. A missing
// observation, failed callback, navigation or unavailable guest proves nothing.
func (list *claudeAccountList) bindDesktop(id string, observation claudeGuestSnapshot) (changed bool, destination string, restart bool) {
	if id == "default" && list.DefaultDesktopRemoved {
		return false, id, false
	}
	owner := list.desktopOwner(id)
	if !observation.LoggedIn {
		if owner != nil && owner.DesktopStatus != "signed_out" {
			owner.DesktopStatus = "signed_out"
			return true, id, false
		}
		return false, id, false
	}
	subject := observation.subject()
	if subject == "" || observation.Organization == "" {
		return false, id, false
	}
	if owner != nil && claudeIdentityHash(owner.Email, owner.Organization) == subject {
		if owner.DesktopStatus != "ready" {
			owner.DesktopStatus = "ready"
			return true, id, false
		}
		return false, id, false
	}
	if owner == nil && id != "default" && id != list.PendingDesktop {
		return false, id, false
	}
	if owner != nil {
		owner.DesktopInstanceID = ""
		owner.DesktopStatus = ""
	}
	if list.PendingDesktop == id {
		list.PendingDesktop = ""
	}
	target := list.bySubject(subject)
	existingCodeProfile := id == "default" && target != nil && validClaudeID(target.guestID())
	if target == nil {
		list.Accounts = append(list.Accounts, claudeAccount{ID: randomToken()[:24], Source: "managed", Status: "signed_out", CodeUnlinked: true, Label: observation.email(), Email: observation.email(), Organization: observation.Organization})
		target = &list.Accounts[len(list.Accounts)-1]
	}
	list.undismissDefault(subject)
	dest := target.DesktopInstanceID
	if dest != "" && dest != id {
		if list.DesktopRedirects == nil {
			list.DesktopRedirects = map[string]string{}
		}
		// Flatten aliases so a viewer holding an older ID can reconnect directly.
		for old, v := range list.DesktopRedirects {
			if v == id {
				list.DesktopRedirects[old] = dest
			}
		}
		list.DesktopRedirects[id] = dest
		delete(list.DesktopLabels, id)
		return true, dest, true
	}
	target.DesktopInstanceID = id
	target.DesktopStatus = "ready"
	// The default Desktop needs a new process to read the existing Code profile.
	return true, id, owner != nil || target.ProxyNodeID != "" || existingCodeProfile
}

func (s *Server) inspectDesktops(ctx context.Context) (map[string]desktopObservation, error) {
	data, err := s.guest(ctx, "exec /usr/bin/python3 -I -c "+shellescape.Quote(desktopInspection))
	var observations map[string]desktopObservation
	if err != nil || json.Unmarshal(data, &observations) != nil {
		return nil, errors.New("无法读取 Desktop 运行状态")
	}
	return observations, nil
}

// Called with the operation lock. Persist the new owner before using its route
// or Code configuration, and close the old execution scope before rebinding it.
func (s *Server) reconcileDesktops(ctx context.Context, observed map[string]desktopObservation) error {
	m := &s.claudeAccounts
	m.mu.Lock()
	list, err := s.loadClaudeAccounts()
	if err == nil && list.migrateDesktopInstances(observed) {
		err = s.saveClaudeAccounts(list)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(observed))
	for id := range observed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := observed[id]
		if !v.Running || v.Identity == nil {
			continue
		}
		m.mu.Lock()
		list, err := s.loadClaudeAccounts()
		if err != nil {
			m.mu.Unlock()
			return err
		}
		if redirect := list.DesktopRedirects[id]; redirect != "" || (id == "default" && list.DefaultDesktopRemoved) {
			m.mu.Unlock()
			// A previously retired default service can be reopened by legacy UI.
			if id == "default" {
				if err := s.stopDesktopInstance(ctx, id); err != nil {
					return err
				}
				v.Running = false
				observed[id] = v
			}
			continue
		}
		changed, dest, restart := list.bindDesktop(id, *v.Identity)
		if !changed {
			m.mu.Unlock()
			continue
		}
		if restart {
			if err := s.stopDesktopInstance(ctx, id); err != nil {
				m.mu.Unlock()
				return err
			}
			v.Running = false
			observed[id] = v
		}
		err = s.saveClaudeAccounts(list)
		var target claudeAccount
		if a := list.desktopOwner(dest); a != nil {
			target = *a
		}
		m.mu.Unlock()
		if err != nil {
			return err
		}
		if restart && !observed[dest].Running && dest != "default" {
			if err := s.changeAccountDesktop(ctx, dest, target, "start"); err != nil {
				return err
			}
			observed[dest] = desktopObservation{Running: true}
		} else if restart && dest == "default" && !observed[dest].Running {
			if err := s.startClaudeDesktop(); err != nil {
				return err
			}
			observed[dest] = desktopObservation{Running: true}
		}
	}
	return nil
}

func (s *Server) stopDesktopInstance(ctx context.Context, id string) error {
	if id == "default" {
		_, err := s.guest(ctx, "sudo systemctl stop cage-claude-desktop.service")
		return err
	}
	return s.changeAccountDesktop(ctx, id, claudeAccount{}, "stop")
}

func (s *Server) desktopCatalog(w http.ResponseWriter, r *http.Request) {
	supported := s.accountDesktopsSupported()
	observed := map[string]desktopObservation{}
	if supported && s.vmRunning() {
		// A list request during startup still returns the observed state. Only
		// the lock holder may change an instance's owner or service.
		canReconcile := s.op.TryLock()
		if canReconcile {
			defer s.op.Unlock()
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		var err error
		observed, err = s.inspectDesktops(ctx)
		if err == nil && canReconcile {
			err = s.reconcileDesktops(ctx, observed)
		}
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	}
	for _, v := range observed {
		if v.Running {
			s.watchDesktopLogins()
			break
		}
	}
	s.claudeAccounts.mu.Lock()
	list, err := s.loadClaudeAccounts()
	s.claudeAccounts.mu.Unlock()
	if err != nil {
		requestError(w, err)
		return
	}
	accounts := make([]desktopEntry, 0, len(list.Accounts))
	instances := make([]desktopEntry, 0, len(list.Accounts)+1)
	add := func(id string) {
		e := list.desktopEntry(id)
		e.Running = observed[id].Running
		e.Generation = observed[id].Generation
		instances = append(instances, e)
	}
	if list.hasDefaultDesktop() {
		add("default")
	}
	for _, a := range list.Accounts {
		e := desktopEntry{ID: a.ID, AccountID: a.ID, Number: a.Number, Label: a.Label, Dedicated: a.DesktopDedicatedNetwork || a.ProxyNodeID != "", Running: observed[a.DesktopInstanceID].Running}
		accounts = append(accounts, e)
		if a.DesktopInstanceID != "" && a.DesktopInstanceID != "default" {
			add(a.DesktopInstanceID)
		}
	}
	if list.PendingDesktop != "" {
		add(list.PendingDesktop)
	}
	jsonReply(w, map[string]any{"supported": supported, "accounts": accounts, "instances": instances, "redirects": list.DesktopRedirects, "default_removed": list.DefaultDesktopRemoved})
}

// The legacy window retains its original HOME, keyring and Desktop data. Add
// the assigned network to that same service when it is next launched.
func (s *Server) defaultDesktopScope(ctx context.Context) (string, error) {
	m := &s.claudeAccounts
	m.mu.Lock()
	list, err := s.loadClaudeAccounts()
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	if list.DefaultDesktopRemoved {
		return "", errors.New("默认 Desktop 已删除，请从实例列表打开其他 Desktop")
	}
	if list.DesktopRedirects["default"] != "" {
		return "", errors.New("此 Desktop 已归入已有实例，请从实例列表打开")
	}
	const directory = "/run/systemd/system/cage-claude-desktop.service.d"
	const override = directory + "/40-account.conf"
	const marker = directory + "/account-scope"
	account := list.desktopOwner("default")
	if account == nil {
		return "sudo rm -f -- " + override + " " + marker + "\n", nil
	}
	mode := "shared"
	config := "[Service]\nBindReadOnlyPaths=" + marker + ":/run/cage-desktop/account-scope\n"
	if account.ProxyNodeID != "" {
		if err := s.ensureAccountNetwork(ctx, account.ID, account.ProxyNodeID); err != nil {
			return "", err
		}
		mode = "node"
		ns := "net-" + account.ID
		config += "NetworkNamespacePath=/run/netns/" + ns + "\nBindReadOnlyPaths=/etc/netns/" + ns + "/resolv.conf:/etc/resolv.conf /etc/netns/" + ns + "/nsswitch.conf:/etc/nsswitch.conf\nInaccessiblePaths=-/run/systemd/resolve/io.systemd.Resolve -/run/dbus/system_bus_socket -/run/nscd/socket\n"
	}
	subject := claudeIdentityHash(account.Email, account.Organization)
	return "sudo install -d -m 0755 " + directory + "\nprintf '%s\\n' " + shellescape.Quote(account.ID+" "+mode+" "+account.guestID()+" "+subject) + " | sudo tee " + marker + " >/dev/null\nprintf %s " + shellescape.Quote(config) + " | sudo tee " + override + " >/dev/null\n", nil
}
