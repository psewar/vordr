package target

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
)

// synology speaks for a Synology NAS through the DSM Web API (entry.cgi), as the control panel does. Requests,
// answers and the export format are those of DSM 7 (design/proxy-agent.md 5).
type synology struct {
	base       string // https://<address>:<port>
	client     *http.Client
	user, pass string
	// poll and pollTimeout: how the export task is watched; tests shorten them.
	poll, pollTimeout time.Duration
}

func newSynology(base string, client *http.Client, creds map[string]string) (*synology, Result) {
	if creds["USER"] == "" || creds["PASS"] == "" {
		return nil, Result{protocol.CodeFailedTargetAuth, "credentials need USER and PASS"}
	}
	return &synology{base: base, client: client, user: creds["USER"], pass: creds["PASS"], poll: time.Second,
		pollTimeout: 3 * time.Minute}, Result{}
}

func (s *synology) Name() string { return "synology-dsm" }

// dsmAnswer is the envelope of every Web API answer.
type dsmAnswer struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code int `json:"code"`
	} `json:"error"`
}

// DSM error codes that mean the account, not the request: no such account or wrong password (400), disabled (401),
// no permission (402, 105), second factor needed (403, 406), blocked (407), expired password (408-410), session gone
// (106, 119).
var dsmAuthCodes = map[int]bool{400: true, 401: true, 402: true, 403: true, 406: true, 407: true, 408: true, 409: true,
	410: true, 105: true, 106: true, 119: true}

// post sends one form request; raw returns the body without decoding (the export download).
func (s *synology) post(ctx context.Context, form url.Values) ([]byte, Result) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/webapi/entry.cgi",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, Result{protocol.CodePrecheckRepoUnreachable, err.Error()}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, transportResult(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, Result{protocol.CodePrecheckRepoUnreachable, err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, Result{protocol.CodePrecheckRepoUnreachable, "DSM answered " + resp.Status}
	}
	return body, Result{}
}

// call sends one API request and decodes the envelope into data.
func (s *synology) call(ctx context.Context, sid string, form url.Values, data any) Result {
	if sid != "" {
		form.Set("_sid", sid)
	}
	body, res := s.post(ctx, form)
	if !res.OK() {
		return res
	}
	var a dsmAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return Result{protocol.CodeFailedPackageManager, fmt.Sprintf("%s: answer is not DSM JSON", form.Get("api"))}
	}
	if !a.Success {
		code := protocol.CodeFailedPackageManager
		if dsmAuthCodes[a.Error.Code] {
			code = protocol.CodeFailedTargetAuth
		}
		return Result{code, fmt.Sprintf("%s %s: DSM error %d", form.Get("api"), form.Get("method"), a.Error.Code)}
	}
	if data != nil && len(a.Data) > 0 {
		if err := json.Unmarshal(a.Data, data); err != nil {
			return Result{protocol.CodeFailedPackageManager, fmt.Sprintf("%s: unexpected data", form.Get("api"))}
		}
	}
	return Result{}
}

func api(name, version, method string, more ...string) url.Values {
	v := url.Values{"api": {name}, "version": {version}, "method": {method}}
	for i := 0; i+1 < len(more); i += 2 {
		v.Set(more[i], more[i+1])
	}
	return v
}

// session logs in, runs f and logs out again. A service account without the admin group may log in only to a
// known application: "webui" (DSM itself).
func (s *synology) session(ctx context.Context, f func(sid string) Result) Result {
	var login struct {
		SID string `json:"sid"`
	}
	if res := s.call(ctx, "", api("SYNO.API.Auth", "6", "login", "account", s.user, "passwd", s.pass,
		"session", "webui", "format", "sid"), &login); !res.OK() {
		return res
	}
	defer s.call(context.WithoutCancel(ctx), login.SID, api("SYNO.API.Auth", "6", "logout", "session", "webui"), nil)
	return f(login.SID)
}

type dsmOffer struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Type      string `json:"type"`
}

func (s *synology) Check(ctx context.Context) (Status, Result) {
	st := Status{Product: "Synology DSM", Facts: map[string]string{}}
	res := s.session(ctx, func(sid string) Result {
		var info struct {
			Model         string `json:"model"`
			VersionString string `json:"version_string"`
		}
		if res := s.call(ctx, sid, api("SYNO.DSM.Info", "2", "getinfo"), &info); !res.OK() {
			return res
		}
		st.Model, st.Installed = info.Model, strings.TrimPrefix(info.VersionString, "DSM ")
		// promotion: a new major version, absent while the device looks for fixes of its version only. The shape of
		// an available offer (version) is assumed; without a version it counts as an unknown one.
		var check struct {
			Update    dsmOffer `json:"update"`
			Promotion dsmOffer `json:"promotion"`
		}
		if res := s.call(ctx, sid, api("SYNO.Core.Upgrade.Server", "3", "check", "need_auto_smallupdate", "true",
			"need_promotion", "true", "user_reading", "true"), &check); !res.OK() {
			return res
		}
		for _, o := range []struct {
			name string
			dsmOffer
		}{{"DSM", check.Update}, {"DSM (new major version)", check.Promotion}} {
			if o.Available {
				v := o.Version
				if v == "" {
					v = UnknownVersion
				}
				st.Offers = append(st.Offers, Offer{Name: o.name, Version: v, Security: strings.Contains(o.Type, "security")})
			}
		}
		var setting struct {
			AutoupdateEnable bool   `json:"autoupdate_enable"`
			AutoupdateType   string `json:"autoupdate_type"`
			UpgradeType      string `json:"upgrade_type"`
		}
		if res := s.call(ctx, sid, api("SYNO.Core.Upgrade.Setting", "2", "get"), &setting); !res.OK() {
			return res
		}
		st.SelfUpdate = "off"
		if setting.AutoupdateEnable {
			st.SelfUpdate = "on (" + setting.AutoupdateType + ")"
		}
		st.Facts["update_check"] = setting.UpgradeType // "hotfix": the device only looks for fixes of its version
		return Result{}
	})
	return st, res
}

// xzMagic starts every DSM configuration export (.dss is xz-compressed).
var xzMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}

// minExport: the smallest export accepted; even a small installation exports tens of KB.
const minExport = 1024

func (s *synology) Backup(ctx context.Context) ([]byte, string, Result) {
	var export []byte
	res := s.session(ctx, func(sid string) Result {
		var task struct {
			TaskID string `json:"task_id"`
			Finish bool   `json:"finish"`
		}
		if res := s.call(ctx, sid, api("SYNO.Backup.Config.Backup", "1", "start"), &task); !res.OK() {
			return Result{protocol.CodeFailedConfigBackup, res.Detail}
		}
		id := task.TaskID
		deadline := time.Now().Add(s.pollTimeout)
		for !task.Finish {
			if time.Now().After(deadline) {
				return Result{protocol.CodeFailedConfigBackup, "export did not finish in time"}
			}
			select {
			case <-ctx.Done():
				return Result{protocol.CodeFailedConfigBackup, "export interrupted"}
			case <-time.After(s.poll):
			}
			if res := s.call(ctx, sid, api("SYNO.Backup.Config.Backup", "1", "status", "task_id", id), &task); !res.OK() {
				return Result{protocol.CodeFailedConfigBackup, res.Detail}
			}
		}
		form := api("SYNO.Backup.Config.Backup", "1", "download", "task_id", id)
		form.Set("_sid", sid)
		body, res := s.post(ctx, form)
		if !res.OK() {
			return Result{protocol.CodeFailedConfigBackup, res.Detail}
		}
		if len(body) < minExport || !bytes.HasPrefix(body, xzMagic) {
			return Result{protocol.CodeFailedConfigBackup,
				fmt.Sprintf("export of %d bytes is not an xz-compressed DSM configuration", len(body))}
		}
		export = body
		return Result{}
	})
	return export, ".dss", res
}
