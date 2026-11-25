// What a machine has for the services a page asks for, and what a person typed for it ( ADR 0026, 0027, 0028 ): asked of the
// agent of that machine -- `svc/packages.go`, `svc/fetch.go`, `svc/diagnose.go`, `svc/devsource.go`, `svc/versions.go` --, and its command line.

package agent

import (
	"fmt"
	"os"
	"time"

	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
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
	case "look":
		return svc.Look(req, a.servingHere()), nil
	case "copy_into": // where a view's copy goes here, and whose it is
		return svc.ViewInto(kit.Text(req["vid"]), svc.ObjectOf(req["origin"]))
	case "copies_remove":
		return svc.RemoveViews(kit.Strs(req["vids"])), nil
	case "compose":
		words, needs, said, err := svc.ComposedHere(key)
		return map[string]any{"words": words, "needs": needs, "text": said}, err
	case "install":
		d, err := svc.InstallService(key, kit.Strs(req["words"]), svc.ObjectOf(req["needs"]))
		return map[string]any{"declaration": d, "file": svc.InstalledFile(nil)}, err
	case "fetch":
		return svc.FetchFrom(kit.Text(req["source"]), kit.Text(req["index"]), kit.Text(req["into"]), kit.Text(req["for"]), kit.Strs(req["source_ok"]))
	case "probe":
		return svc.ProbeProgram(kit.Strs(req["argv"]))
	case "dev_ensure":
		var want svc.AskedService
		if err := reach.DecodeInto(req["want"], &want); err != nil {
			return nil, err
		}
		key, err := a.devEnsure(kit.Text(req["name"]), want)
		return map[string]any{"key": key}, err
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
		host, _ := os.Hostname()
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
