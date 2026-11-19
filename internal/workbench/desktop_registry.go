package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
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
	return nil
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
	if a := list.desktopOwner(id); a != nil {
		return desktopEntry{ID: id, AccountID: a.ID, Number: a.Number, Label: a.Label, Dedicated: a.DesktopDedicatedNetwork || a.ProxyNodeID != ""}
	}
	label := "待登录 Desktop"
	if id == "default" {
		label = "默认 Desktop"
	}
	return desktopEntry{ID: id, Label: label}
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
		if redirect := list.DesktopRedirects[id]; redirect != "" {
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
	if _, exists := observed["default"]; (exists || list.desktopOwner("default") != nil) && list.DesktopRedirects["default"] == "" {
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
	jsonReply(w, map[string]any{"supported": supported, "accounts": accounts, "instances": instances, "redirects": list.DesktopRedirects})
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
