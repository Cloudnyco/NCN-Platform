// In-chat server / network management — the inline menus under /netadmin.
//
// Mirrors bot_manage.go (alert rules) but for the node lifecycle, so ops can
// configure the network from a phone: list servers, decommission/recommission
// (reversible → tap-confirm), apply mesh config, and permanently delete
// (irreversible / live-routing → require a TYPED confirm word in chat:
// "DELETE <id>" / "APPLY MESH <id>"). Gated to the ops group (handleUpdate
// checks the chat) and every mutation is audited. Reuses the shared fleet
// methods f.decommission/recommission/deleteNode/beginMeshApply (nodes_api.go,
// meshApply.go) so the bot and the HTTP console share one code path. Provision
// / add-node stays on the web console (SSH password + many fields).

package main

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"
)

const netConfirmTTL = 2 * time.Minute

// pendingNetAction is a high-risk op (delete / mesh) awaiting its typed confirm
// word in the chat. Single-use, TTL'd.
type pendingNetAction struct {
	kind   string // "del" | "mesh"
	target string
	word   string // the exact text the user must send
	exp    time.Time
}

// nodeStatusByID returns a map of id → live scrape status for badges.
func (n *tgNotifier) nodeStatusByID() map[string]*fleetNodeStatus {
	m := map[string]*fleetNodeStatus{}
	if n.fleet == nil {
		return m
	}
	for _, s := range n.fleet.snapshotNodes() {
		if s != nil {
			m[s.Node.ID] = s
		}
	}
	return m
}

func (n *tgNotifier) netRootView() (string, map[string]any) {
	recs := n.fleet.registry.listSnapshot()
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	st := n.nodeStatusByID()
	rows := [][]tgKbBtn{}
	active := 0
	for _, r := range recs {
		icon := "•"
		if r.Status == nodeStatusDecommissioned {
			icon = "⛔"
		} else {
			active++
			if s := st[r.ID]; s != nil {
				if s.OK {
					icon = "🟢"
				} else {
					icon = "🔴"
				}
			} else {
				icon = "⚪"
			}
		}
		rows = append(rows, []tgKbBtn{{Text: fmt.Sprintf("%s %s · %s", icon, r.ID, r.Label), Data: "nd:s:" + r.ID}})
	}
	text := fmt.Sprintf("<b>⚙ Server admin</b>\n%d registered / %d active · tap one to decommission/restore/mesh/delete", len(recs), active)
	return text, kbMarkup(rows)
}

func (n *tgNotifier) netNodeView(id string) (string, map[string]any) {
	rec, ok := n.fleet.registry.get(id)
	if !ok {
		return n.netRootView()
	}
	st := n.nodeStatusByID()[id]
	statusLine := "active"
	if rec.Status == nodeStatusDecommissioned {
		statusLine = "decommissioned"
	}
	live := "—"
	if st != nil {
		if st.OK {
			live = "🟢 reachable"
		} else {
			live = fmt.Sprintf("🔴 down (×%d)", st.ConsecFail)
		}
		if st.AgentCertDaysLeft != 0 {
			live += fmt.Sprintf(" · cert %dd", st.AgentCertDaysLeft)
		}
	}
	text := fmt.Sprintf("<b>%s</b> <code>%s</code>\naddr: <code>%s</code> · region %d\nstatus: %s · %s",
		html.EscapeString(rec.Label), html.EscapeString(rec.ID), html.EscapeString(rec.Address), rec.Region, statusLine, live)

	rows := [][]tgKbBtn{}
	if id == n.fleet.localID {
		text += "\n\n<i>local console node — cannot decommission/delete</i>"
	} else if rec.Status == nodeStatusDecommissioned {
		rows = append(rows, []tgKbBtn{{Text: "⬆️ Restore", Data: "nd:rec:" + id}, {Text: "🗑 Delete", Data: "nd:del:" + id}})
	} else {
		rows = append(rows,
			[]tgKbBtn{{Text: "🔗 Mesh", Data: "nd:mesh:" + id}},
			[]tgKbBtn{{Text: "⬇️ Decommission", Data: "nd:dec:" + id}, {Text: "🗑 Delete", Data: "nd:del:" + id}})
	}
	rows = append(rows, []tgKbBtn{{Text: "⬅ Back", Data: "nd:root"}})
	return text, kbMarkup(rows)
}

func (n *tgNotifier) netConfirmView(id, verb, okData string) (string, map[string]any) {
	text := fmt.Sprintf("Confirm <b>%s</b> <code>%s</code> ?", verb, html.EscapeString(id))
	return text, kbMarkup([][]tgKbBtn{{{Text: "✅ Confirm", Data: okData}, {Text: "Cancel", Data: "nd:s:" + id}}})
}

// replyNetRoot — the /netadmin command entry (fresh menu message).
func (n *tgNotifier) replyNetRoot() {
	if n.fleet == nil {
		n.reply("fleet not ready")
		return
	}
	text, mk := n.netRootView()
	n.enqueue(tgPayload{Text: text, Disabled: true, ReplyMarkup: mk}, "netadmin root")
}

// pendKey scopes a pending confirm to one operator in one chat, so two
// operators arming DELETE/APPLY MESH in the same group can't cross-confirm
// each other's action.
func pendKey(chat string, userID int64) string {
	return chat + ":" + strconv.FormatInt(userID, 10)
}

func (n *tgNotifier) armPending(chat string, userID int64, kind, target, word string) {
	n.pendingMu.Lock()
	n.pendingNet[pendKey(chat, userID)] = &pendingNetAction{kind: kind, target: target, word: word, exp: time.Now().Add(netConfirmTTL)}
	n.pendingMu.Unlock()
}

// handleNetConfirm is called from the message path BEFORE the command switch.
// Returns true if it consumed the message (matched THIS user's pending confirm
// word). op is the resolved operator account (audit actor). Result replies go
// to n.cmdChat (the originating chat), set by handleUpdate.
func (n *tgNotifier) handleNetConfirm(chat string, userID int64, op, text string) bool {
	key := pendKey(chat, userID)
	n.pendingMu.Lock()
	pa := n.pendingNet[key]
	if pa == nil || time.Now().After(pa.exp) {
		if pa != nil {
			delete(n.pendingNet, key)
		}
		n.pendingMu.Unlock()
		return false
	}
	if strings.TrimSpace(text) != pa.word {
		n.pendingMu.Unlock()
		return false // not the confirm word — let normal handling proceed (pending stays until TTL)
	}
	delete(n.pendingNet, key)
	n.pendingMu.Unlock()

	f := n.fleet
	switch pa.kind {
	case "del":
		if err := f.deleteNode(pa.target, "tg:"+op); err != nil {
			n.reply("delete failed: " + html.EscapeString(err.Error()))
		} else {
			n.reply("🗑 Permanently deleted <code>" + html.EscapeString(pa.target) + "</code>")
		}
	case "mesh":
		// Default bot behaviour: weave the node + ALL active peers, GRE.
		targets := []string{pa.target}
		for _, r := range f.registry.listSnapshot() {
			if r.ID != pa.target && r.active() {
				targets = append(targets, r.ID)
			}
		}
		if _, err := f.beginMeshApply(pa.target, targets, nil, 0, "tg:"+op); err != nil {
			n.reply("mesh apply failed: " + html.EscapeString(err.Error()))
		} else {
			n.reply("🔗 Started meshing <code>" + html.EscapeString(pa.target) + "</code> (all active peers · GRE) — result to follow")
		}
	}
	return true
}

// handleNetCallback dispatches the nd: inline-menu callbacks. userID scopes the
// armed confirm word to the tapping operator; op is the audit actor.
func (n *tgNotifier) handleNetCallback(ctx context.Context, cbID, chat string, msgID int64, data string, userID int64, op string) {
	if n.fleet == nil {
		n.answerCallback(ctx, cbID, "fleet not ready")
		return
	}
	toast := ""
	switch {
	case data == "nd:root":
	case strings.HasPrefix(data, "nd:s:"):
	case strings.HasPrefix(data, "nd:dec:"):
		id := strings.TrimPrefix(data, "nd:dec:")
		text, mk := n.netConfirmView(id, "decommission", "nd:decok:"+id)
		n.editMessage(ctx, chat, msgID, text, mk)
		n.answerCallback(ctx, cbID, "")
		return
	case strings.HasPrefix(data, "nd:rec:"):
		id := strings.TrimPrefix(data, "nd:rec:")
		text, mk := n.netConfirmView(id, "restore", "nd:recok:"+id)
		n.editMessage(ctx, chat, msgID, text, mk)
		n.answerCallback(ctx, cbID, "")
		return
	case strings.HasPrefix(data, "nd:decok:"):
		id := strings.TrimPrefix(data, "nd:decok:")
		if _, err := n.fleet.decommission(id, "tg:"+op); err != nil {
			toast = err.Error()
		} else {
			toast = id + " decommissioned"
		}
		data = "nd:s:" + id
	case strings.HasPrefix(data, "nd:recok:"):
		id := strings.TrimPrefix(data, "nd:recok:")
		if _, err := n.fleet.recommission(id, "tg:"+op); err != nil {
			toast = err.Error()
		} else {
			toast = id + " restored"
		}
		data = "nd:s:" + id
	case strings.HasPrefix(data, "nd:del:"):
		id := strings.TrimPrefix(data, "nd:del:")
		n.armPending(chat, userID, "del", id, "DELETE "+id)
		n.answerCallback(ctx, cbID, "Danger! send  DELETE "+id+"  to confirm")
		text, mk := n.netNodeView(id)
		n.editMessage(ctx, chat, msgID, text+"\n\n⚠️ Permanent delete: send <code>DELETE "+html.EscapeString(id)+"</code> to confirm (within 2 min)", mk)
		return
	case strings.HasPrefix(data, "nd:mesh:"):
		id := strings.TrimPrefix(data, "nd:mesh:")
		n.armPending(chat, userID, "mesh", id, "APPLY MESH "+id)
		n.answerCallback(ctx, cbID, "send  APPLY MESH "+id+"  to confirm")
		text, mk := n.netNodeView(id)
		n.editMessage(ctx, chat, msgID, text+"\n\n🔗 Mesh (changes live routing): send <code>APPLY MESH "+html.EscapeString(id)+"</code> to confirm (within 2 min)", mk)
		return
	default:
		n.answerCallback(ctx, cbID, "?")
		return
	}

	var text string
	var mk map[string]any
	if strings.HasPrefix(data, "nd:s:") {
		text, mk = n.netNodeView(strings.TrimPrefix(data, "nd:s:"))
	} else {
		text, mk = n.netRootView()
	}
	n.editMessage(ctx, chat, msgID, text, mk)
	n.answerCallback(ctx, cbID, toast)
}
