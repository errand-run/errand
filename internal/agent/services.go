// What a machine has for a page's service, and what a person authorized or typed for it ( ADR 0027, 0028, 0041 ): asked of the
// agent of that machine -- `svc/pagedesc.go`, `svc/fetch.go`, `svc/diagnose.go`, `svc/versions.go` --, and of its command line.

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/system"
)

// servingHere: the services the agents of this machine hold, and how each is
func (a *Place) servingHere() map[string]string {
	out := map[string]string{}
	for _, r := range overview(a).Serving {
		out[r.Name] = r.State
	}
	return out
}

func (a *Place) servicesDo(req map[string]any, f *reach.FarChan) {
	got, err := a.serviceAnswer(svc.ObjectOf(req["req"]))
	if err != nil {
		f.Fail("%v", err)
		return
	}
	got["ok"] = true
	f.Reply(got)
}

func (a *Place) serviceAnswer(req map[string]any) (map[string]any, error) {
	key := kit.Text(req["key"])
	switch kit.Text(req["do"]) {
	case "page_look": // what this machine has for a page's description, and lacks ( ADR 0041 )
		d, err := pageDesc(req)
		if err != nil {
			return nil, err
		}
		return map[string]any{"look": svc.LookPage(d, pathsOf(req), svc.ObjectOf(req["store"]))}, nil
	case "dev_declare": // a checkout stands for a repository here ( a person said so, in errand's page )
		return map[string]any{"ok": true}, svc.DeclareCheckout(kit.Text(req["repo"]), kit.Text(req["path"]), kit.Text(req["env"]))
	case "store_seen": // where this machine sees a view's files
		return map[string]any{"path": svc.StoreSeen(svc.ObjectOf(req["store"]))}, nil
	case "copy_into": // where a view's copy goes here, and whose it is
		return svc.ViewInto(kit.Text(req["vid"]), svc.ObjectOf(req["origin"]))
	case "copies_remove":
		return svc.RemoveViews(kit.Strs(req["vids"])), nil
	case "authorize": // a page's description, or a server in full
		if req["described"] != nil {
			d, err := pageDesc(req)
			if err != nil {
				return nil, err
			}
			l, err := svc.AuthorizePage(d, pathsOf(req), kit.Strs(req["source_ok"]), func(string) {})
			a.stir()
			return map[string]any{"look": l, "file": svc.AuthorizedFile()}, err
		}
		var spec svc.ServiceSpec
		if err := reach.DecodeInto(req["spec"], &spec); err != nil || len(spec.Command) == 0 {
			return nil, errors.New("authorize: a page's description, or a server in full")
		}
		hash, err := svc.Authorize(spec, kit.Text(req["to"]), svc.Authorization{})
		a.stir()
		return map[string]any{"hash": hash, "definition": svc.Definition(spec), "file": svc.AuthorizedFile()}, err
	case "withdraw":
		_, err := svc.Withdraw(kit.Text(req["hash"]), kit.Text(req["to"]))
		a.stir()
		return map[string]any{"file": svc.AuthorizedFile()}, err
	case "authorized":
		all, err := svc.Authorized()
		return map[string]any{"authorized": all, "file": svc.AuthorizedFile()}, err
	case "fetch":
		return svc.FetchFrom(kit.Text(req["source"]), kit.Text(req["index"]), kit.Text(req["into"]), kit.Text(req["for"]), kit.Strs(req["source_ok"]))
	case "probe":
		return svc.ProbeProgram(kit.Strs(req["argv"]))
	case "versions":
		versions, code := svc.VersionsHere(a.servingHere(), true, svc.VersionDays)
		return map[string]any{"versions": versions, "code": code, "days": svc.VersionDays}, nil
	case "pin", "unpin":
		return map[string]any{"pinned": req["do"] == "pin"}, svc.PinVersion(key, req["do"] == "pin")
	case "prune":
		return svc.PruneVersions(a.servingHere(), kit.Num(req["unused"], svc.VersionDays), req["dry_run"] == true), nil
	}
	return nil, fmt.Errorf("%q is nothing the services of a machine are asked", kit.Text(req["do"]))
}

// pageDesc: the description a request carries
func pageDesc(req map[string]any) (svc.PageDesc, error) {
	b, _ := json.Marshal(req["described"])
	d, _, err := svc.ParsePageDesc(b)
	return d, err
}

// pathsOf: the places a person gave for a description's tools
func pathsOf(req map[string]any) map[string][]string {
	out := map[string][]string{}
	for tool, x := range svc.ObjectOf(req["paths"]) {
		out[tool] = kit.Strs(x)
	}
	return out
}

// copyView: a view's files, pushed into errand's cache on another machine
func (a *Place) copyView(req map[string]any, f *reach.FarChan) {
	var h reach.HostSpec
	if reach.DecodeInto(req["host"], &h) != nil || h.Host == "" {
		f.Fail("no machine to copy to")
		return
	}
	t := &reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}
	defer t.Close()
	note := filesync.CopyNote{ID: filesync.CopyNoteID(kit.Text(req["dir"]), h), Path: kit.Text(req["dir"]), Host: h, Machine: h.Host, Vid: kit.Text(req["vid"]), State: "intended", At: kit.Seconds(time.Now())}
	if name := kit.Text(req["machine"]); name != "" {
		note.Machine = name
	}
	if !kit.Exists(note.File()) { // written before the other machine is told anything
		note.Write()
	}
	l, err := reach.LinkTo(t, &h)
	var root string
	if err == nil {
		host := system.Hostname()
		var got map[string]any
		got, err = l.Ask(map[string]any{"op": "services_do", "req": map[string]any{"do": "copy_into", "vid": req["vid"],
			"origin": map[string]any{"host": host, "path": req["dir"]}}})
		root = kit.Text(got["root"])
	}
	if err == nil {
		all, _ := filesync.NewSelection(nil, false, nil)
		_, err = filesync.Push(l, kit.Text(req["dir"]), root, all, "", h.Host, true, func(string) {}) // ( errand's own directory )
	}
	if err != nil {
		f.Fail("%v", err)
		return
	}
	note.State, note.There = "there", root
	note.Write()
	f.Reply(map[string]any{"ok": true, "root": root})
}
