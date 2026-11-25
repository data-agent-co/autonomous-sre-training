package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The dashboard has its own panel file, web/dashboard.json, and does not read
// the Perses dashboard. These tests keep the two honest:
//
//   - TestDashboardFileIsValid checks the file against the shape the page
//     expects, so a typo fails here and not as a blank panel in the browser.
//   - TestPanelsMatchPerses fails when a panel, query, unit, threshold or grid
//     position differs from ../perses/demo-cluster-overview.json. To change a
//     panel, change both files.
//
// What may differ on purpose: descriptions, colours, and the Perses Markdown
// panel "topology", which this dashboard replaces with the live topology.

const (
	dashboardPath = "web/dashboard.json"
	persesPath    = "../perses/demo-cluster-overview.json"

	// persesTopologyPanel is the Markdown panel with no counterpart here.
	persesTopologyPanel = "topology"
	gridColumns         = 24
)

type dashboardFile struct {
	Version     int            `json:"version"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Range       string         `json:"range"`
	Refresh     string         `json:"refresh"`
	MinStep     string         `json:"minStep"`
	Palette     map[string]int `json:"palette"`
	Topology    topologySpec   `json:"topology"`
	Panels      []panelSpec    `json:"panels"`
}

type panelSpec struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Kind         string         `json:"kind"`
	Grid         gridSpec       `json:"grid"`
	Unit         string         `json:"unit"`
	Decimals     *int           `json:"decimals"`
	Min          *float64       `json:"min"`
	Max          *float64       `json:"max"`
	Draw         string         `json:"draw"`
	ConnectNulls bool           `json:"connectNulls"`
	Fill         *float64       `json:"fill"`
	Legend       *legendSpec    `json:"legend"`
	Thresholds   *thresholdSpec `json:"thresholds"`
	Sparkline    bool           `json:"sparkline"`
	Label        bool           `json:"label"`
	Hero         bool           `json:"hero"`
	Columns      []columnSpec   `json:"columns"`
	Empty        string         `json:"empty"`
	Queries      []querySpec    `json:"queries"`
}

type gridSpec struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type legendSpec struct {
	Position string   `json:"position"`
	Values   []string `json:"values"`
}

type thresholdSpec struct {
	Base  string     `json:"base"`
	Steps []stepSpec `json:"steps"`
}

type stepSpec struct {
	From float64 `json:"from"`
	Tone string  `json:"tone"`
}

type columnSpec struct {
	Label  string        `json:"label"`
	Header string        `json:"header"`
	Width  int           `json:"width"`
	Map    []cellMapSpec `json:"map"`
}

type cellMapSpec struct {
	Match string `json:"match"`
	Text  string `json:"text"`
	Tone  string `json:"tone"`
}

type querySpec struct {
	Expr    string   `json:"expr"`
	Legend  string   `json:"legend"`
	Instant bool     `json:"instant"`
	Color   any      `json:"color"` // a slot 1..8, or a tone
	Dash    bool     `json:"dash"`
	Fill    *float64 `json:"fill"`
	Unit    string   `json:"unit"`
}

type topologySpec struct {
	Title        string              `json:"title"`
	Namespace    string              `json:"namespace"`
	Source       sourceSpec          `json:"source"`
	Nodes        []nodeSpec          `json:"nodes"`
	Ports        map[string]portSpec `json:"ports"`
	ControlPlane controlPlaneSpec    `json:"controlPlane"`
	Notes        string              `json:"notes"`
}

type sourceSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Sub   string `json:"sub"`
	Icon  string `json:"icon"`
	Ready string `json:"ready"`
}

type nodeSpec struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Sub     string `json:"sub"`
	Icon    string `json:"icon"`
	Ready   string `json:"ready"`
	Waiting string `json:"waiting"`
	// A container that is down after an OOMKill, and one that restarted
	// after one moments ago.
	OOMKilledNow string       `json:"oomKilledNow"`
	OOMKilled    string       `json:"oomKilled"`
	Memory       string       `json:"memory"`
	Restarts     string       `json:"restarts"`
	Traffic      *trafficSpec `json:"traffic"`
	Ports        []string     `json:"ports"`
	MetricsUp    string       `json:"metricsUp"`
	Fault        *faultSpec   `json:"fault"`
}

type trafficSpec struct {
	Rate   string `json:"rate"`
	Errors string `json:"errors"`
}

type faultSpec struct {
	Badge   string `json:"badge"`
	Name    string `json:"name"`
	Active  string `json:"active"`
	Trigger string `json:"trigger"`
}

type portSpec struct {
	Legend string `json:"legend"`
	Up     string `json:"up"`
}

// The control plane is the layer under the apps: boxes for what reads the
// apps and for where the findings go, and the links between the boxes.
type controlPlaneSpec struct {
	Title string     `json:"title"`
	Note  string     `json:"note"`
	Boxes []boxSpec  `json:"boxes"`
	Links []linkSpec `json:"links"`
}

type boxSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Sub   string `json:"sub"`
	Icon  string `json:"icon"`
	// The kind of port whose line runs into the box.
	Reads  string      `json:"reads"`
	Wide   bool        `json:"wide"`
	Access *accessSpec `json:"access"`
	Stages []stageSpec `json:"stages"`
	Rows   []rowSpec   `json:"rows"`
	List   *listSpec   `json:"list"`
}

type accessSpec struct {
	Label string `json:"label"`
	Title string `json:"title"`
	Write bool   `json:"write"`
}

// stateSpec holds the expressions that give a stage or a row its state, and
// the texts it has for a state. The first of failed, active, ok and set that
// answers a value above 0 wins; up stands for a health check on its own.
// With none above 0 the entry is idle, and needs says what that reading
// leans on: the idle text is used only while needs answers a value above 0.
type stateSpec struct {
	Up         string `json:"up"`
	Failed     string `json:"failed"`
	Active     string `json:"active"`
	OK         string `json:"ok"`
	Set        string `json:"set"`
	Needs      string `json:"needs"`
	IdleText   string `json:"idleText"`
	ActiveText string `json:"activeText"`
	FailedText string `json:"failedText"`
}

type stageSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Title string `json:"title"`
	stateSpec
}

type rowSpec struct {
	ID    string    `json:"id"`
	Label string    `json:"label"`
	Text  string    `json:"text"`
	Icon  string    `json:"icon"`
	Info  *infoSpec `json:"info"`
	stateSpec
}

type infoSpec struct {
	Expr string `json:"expr"`
	Text string `json:"text"`
}

// A list shows the samples of expr. With no sample it says "none" only
// while needs answers a value above 0. tagText says what a row's tag is.
type listSpec struct {
	Title   string `json:"title"`
	Scope   string `json:"scope"`
	Expr    string `json:"expr"`
	Needs   string `json:"needs"`
	Limit   int    `json:"limit"`
	Empty   string `json:"empty"`
	TagText string `json:"tagText"`
}

// A link is live while active answers a value above 0 and ready while ok
// does. Otherwise it is off, and says so only while needs answers a value
// above 0. activeText is its word while live.
type linkSpec struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Label      string `json:"label"`
	Title      string `json:"title"`
	Active     string `json:"active"`
	OK         string `json:"ok"`
	Needs      string `json:"needs"`
	ActiveText string `json:"activeText"`
}

var (
	durationPattern = regexp.MustCompile(`^[1-9][0-9]*[smh]$`)
	idPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

	tones       = []string{"good", "warning", "critical", "info"}
	units       = []string{"decimal", "percent", "requests/sec", "ops/sec", "milliseconds", "seconds", "bytes/sec"}
	legendCalcs = []string{"last", "mean", "max"}
)

// loadDashboard reads web/dashboard.json and rejects fields the page does not
// know, which is how a misspelt field name gets caught.
func loadDashboard(t *testing.T) dashboardFile {
	t.Helper()
	raw, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatalf("read %s: %v", dashboardPath, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d dashboardFile
	if err := dec.Decode(&d); err != nil {
		t.Fatalf("parse %s: %v", dashboardPath, err)
	}
	return d
}

func TestDashboardFileIsValid(t *testing.T) {
	d := loadDashboard(t)

	if d.Version != 1 {
		t.Errorf("version = %d, want 1", d.Version)
	}
	if d.Title == "" {
		t.Error("title is empty")
	}
	for name, value := range map[string]string{"range": d.Range, "refresh": d.Refresh, "minStep": d.MinStep} {
		if !durationPattern.MatchString(value) {
			t.Errorf("%s = %q, want digits followed by s, m or h", name, value)
		}
	}
	for name, slot := range d.Palette {
		if slot < 1 || slot > 8 {
			t.Errorf("palette[%q] = %d, want a slot 1..8", name, slot)
		}
	}
	if len(d.Panels) == 0 {
		t.Fatal("no panels")
	}

	seen := map[string]bool{}
	for _, p := range d.Panels {
		t.Run(p.ID, func(t *testing.T) {
			if !idPattern.MatchString(p.ID) {
				t.Errorf("id %q is not snake_case", p.ID)
			}
			if seen[p.ID] {
				t.Errorf("id %q is used twice", p.ID)
			}
			seen[p.ID] = true
			if p.Title == "" {
				t.Error("title is empty")
			}
			checkGrid(t, p.Grid)
			if len(p.Queries) == 0 {
				t.Error("no queries")
			}
			for i, q := range p.Queries {
				checkQuery(t, i, q, p.Kind)
			}
			checkThresholds(t, p.Thresholds)
			switch p.Kind {
			case "stat":
				checkUnit(t, p.Unit)
				if p.Thresholds == nil {
					t.Error("a stat needs thresholds: they give the tile its tone")
				}
			case "timeseries":
				checkUnit(t, p.Unit)
				if p.Draw != "line" && p.Draw != "bars" {
					t.Errorf("draw = %q, want line or bars", p.Draw)
				}
				if p.Min != nil && p.Max != nil && *p.Min >= *p.Max {
					t.Errorf("min %v is not below max %v", *p.Min, *p.Max)
				}
				checkLegend(t, p.Legend)
			case "table":
				if len(p.Columns) == 0 {
					t.Error("a table needs columns")
				}
				for _, c := range p.Columns {
					if c.Label == "" || c.Header == "" {
						t.Errorf("column %+v needs a label and a header", c)
					}
					for _, m := range c.Map {
						if m.Match == "" || m.Text == "" || !slices.Contains(tones, m.Tone) {
							t.Errorf("column %q: bad map entry %+v", c.Label, m)
						}
					}
				}
			default:
				t.Errorf("kind = %q, want stat, timeseries or table", p.Kind)
			}
		})
	}

	checkNoOverlap(t, d.Panels)
	if heroes := countHeroes(d.Panels); heroes > 1 {
		t.Errorf("%d hero panels, want at most one", heroes)
	}
}

func checkGrid(t *testing.T, g gridSpec) {
	t.Helper()
	if g.X < 0 || g.Y < 0 || g.W < 1 || g.H < 1 || g.X+g.W > gridColumns {
		t.Errorf("grid %+v does not fit a %d-column grid", g, gridColumns)
	}
}

func checkUnit(t *testing.T, unit string) {
	t.Helper()
	if !slices.Contains(units, unit) {
		t.Errorf("unit = %q, want one of %v", unit, units)
	}
}

func checkQuery(t *testing.T, i int, q querySpec, kind string) {
	t.Helper()
	if strings.TrimSpace(q.Expr) == "" {
		t.Errorf("query %d: expr is empty", i)
	}
	if q.Instant != (kind == "table") {
		t.Errorf("query %d: instant = %v in a %s panel (tables use instant queries, the others range queries)", i, q.Instant, kind)
	}
	if q.Unit != "" {
		checkUnit(t, q.Unit)
	}
	switch c := q.Color.(type) {
	case nil:
	case float64:
		if c != float64(int(c)) || c < 1 || c > 8 {
			t.Errorf("query %d: color = %v, want a slot 1..8 or a tone", i, c)
		}
	case string:
		if !slices.Contains(tones, c) {
			t.Errorf("query %d: color = %q, want a slot 1..8 or one of %v", i, c, tones)
		}
	default:
		t.Errorf("query %d: color = %v, want a slot 1..8 or a tone", i, c)
	}
	for name, v := range map[string]*float64{"fill": q.Fill} {
		if v != nil && (*v < 0 || *v > 1) {
			t.Errorf("query %d: %s = %v, want 0..1", i, name, *v)
		}
	}
}

func checkThresholds(t *testing.T, th *thresholdSpec) {
	t.Helper()
	if th == nil {
		return
	}
	if !slices.Contains(tones, th.Base) {
		t.Errorf("thresholds.base = %q, want one of %v", th.Base, tones)
	}
	for i, s := range th.Steps {
		if !slices.Contains(tones, s.Tone) {
			t.Errorf("thresholds.steps[%d].tone = %q, want one of %v", i, s.Tone, tones)
		}
		if i > 0 && s.From <= th.Steps[i-1].From {
			t.Errorf("thresholds.steps[%d].from = %v does not ascend", i, s.From)
		}
	}
}

func checkLegend(t *testing.T, l *legendSpec) {
	t.Helper()
	if l == nil {
		return
	}
	if l.Position != "bottom" && l.Position != "right" {
		t.Errorf("legend.position = %q, want bottom or right", l.Position)
	}
	for _, v := range l.Values {
		if !slices.Contains(legendCalcs, v) {
			t.Errorf("legend.values has %q, want only %v", v, legendCalcs)
		}
	}
}

func checkNoOverlap(t *testing.T, panels []panelSpec) {
	t.Helper()
	for i, a := range panels {
		for _, b := range panels[i+1:] {
			if a.Grid.X < b.Grid.X+b.Grid.W && b.Grid.X < a.Grid.X+a.Grid.W &&
				a.Grid.Y < b.Grid.Y+b.Grid.H && b.Grid.Y < a.Grid.Y+a.Grid.H {
				t.Errorf("panels %s and %s overlap on the grid", a.ID, b.ID)
			}
		}
	}
}

func countHeroes(panels []panelSpec) int {
	n := 0
	for _, p := range panels {
		if p.Hero {
			n++
		}
	}
	return n
}

func TestTopologyIsValid(t *testing.T) {
	topo := loadDashboard(t).Topology

	if topo.Title == "" || topo.Namespace == "" || topo.Notes == "" {
		t.Error("topology needs a title, a namespace and notes")
	}
	if topo.Source.ID == "" || topo.Source.Label == "" || topo.Source.Icon == "" || topo.Source.Ready == "" {
		t.Errorf("topology.source is incomplete: %+v", topo.Source)
	}
	for kind, port := range topo.Ports {
		if port.Legend == "" {
			t.Errorf("ports.%s needs a legend", kind)
		}
	}
	if len(topo.Nodes) == 0 {
		t.Fatal("topology has no nodes")
	}

	ids := map[string]bool{topo.Source.ID: true}
	badges := map[string]bool{}
	for _, n := range topo.Nodes {
		t.Run(n.ID, func(t *testing.T) {
			if ids[n.ID] {
				t.Errorf("id %q is used twice", n.ID)
			}
			ids[n.ID] = true
			for name, value := range map[string]string{
				"label": n.Label, "sub": n.Sub, "icon": n.Icon, "ready": n.Ready,
				"waiting": n.Waiting, "oomKilledNow": n.OOMKilledNow, "oomKilled": n.OOMKilled,
				"memory": n.Memory, "restarts": n.Restarts,
			} {
				if strings.TrimSpace(value) == "" {
					t.Errorf("%s is empty", name)
				}
			}
			if n.Traffic != nil && (n.Traffic.Rate == "" || n.Traffic.Errors == "") {
				t.Errorf("traffic needs rate and errors: %+v", *n.Traffic)
			}
			for _, kind := range n.Ports {
				if _, ok := topo.Ports[kind]; !ok {
					t.Errorf("port %q is not defined under topology.ports", kind)
				}
			}
			if slices.Contains(n.Ports, "metrics") != (n.MetricsUp != "") {
				t.Errorf("metricsUp and the metrics port go together: ports = %v, metricsUp = %q", n.Ports, n.MetricsUp)
			}
			if f := n.Fault; f != nil {
				if f.Badge == "" || f.Name == "" || f.Active == "" || f.Trigger == "" {
					t.Errorf("fault is incomplete: %+v", *f)
				}
				if badges[f.Badge] {
					t.Errorf("fault badge %q is used twice", f.Badge)
				}
				badges[f.Badge] = true
				if _, err := os.Stat(strings.Replace(f.Trigger, "./scripts/", "../scripts/", 1)); err != nil {
					t.Errorf("fault trigger %q: %v", f.Trigger, err)
				}
			}
		})
	}
	if topo.Ports["scan"].Up == "" {
		t.Error("ports.scan needs an up expression: the scan port is the same for every node")
	}

	t.Run("controlPlane", func(t *testing.T) { checkControlPlane(t, topo) })
}

func checkControlPlane(t *testing.T, topo topologySpec) {
	plane := topo.ControlPlane
	if plane.Title == "" || plane.Note == "" {
		t.Error("the control plane needs a title and a note")
	}
	if len(plane.Boxes) < 3 {
		t.Errorf("%d boxes, want what reads the apps and where the findings go: at least 3", len(plane.Boxes))
	}

	boxes := map[string]bool{}
	readers := map[string]string{}
	for _, b := range plane.Boxes {
		t.Run(b.ID, func(t *testing.T) {
			if b.ID == "" || boxes[b.ID] {
				t.Errorf("box id %q is empty or used twice", b.ID)
			}
			boxes[b.ID] = true
			if b.Label == "" || b.Icon == "" {
				t.Errorf("box needs a label and an icon: label = %q, icon = %q", b.Label, b.Icon)
			}
			if b.Reads != "" {
				if _, ok := topo.Ports[b.Reads]; !ok {
					t.Errorf("reads = %q, which is not defined under topology.ports", b.Reads)
				}
				if other, taken := readers[b.Reads]; taken {
					t.Errorf("reads = %q, which %q reads already: a kind of port has one line", b.Reads, other)
				}
				readers[b.Reads] = b.ID
			}
			if a := b.Access; a != nil && (a.Label == "" || a.Title == "") {
				t.Errorf("access needs a label and a title: %+v", *a)
			}

			stages := map[string]bool{}
			for _, s := range b.Stages {
				checkEntry(t, "stage", s.ID, s.Label, stages)
				checkState(t, "stage "+s.ID, s.stateSpec)
			}
			rows := map[string]bool{}
			for _, r := range b.Rows {
				checkEntry(t, "row", r.ID, r.Label, rows)
				checkState(t, "row "+r.ID, r.stateSpec)
				if r.Info != nil && (strings.TrimSpace(r.Info.Expr) == "" || r.Info.Text == "") {
					t.Errorf("row %s: info needs an expr and a text: %+v", r.ID, *r.Info)
				}
			}
			if l := b.List; l != nil {
				if strings.TrimSpace(l.Expr) == "" || l.Title == "" || l.Empty == "" {
					t.Errorf("list needs an expr, a title and an empty text: %+v", *l)
				}
				if l.Limit < 1 {
					t.Errorf("list.limit = %d, want at least 1", l.Limit)
				}
			}
		})
	}

	links := map[string]bool{}
	for _, l := range plane.Links {
		checkEntry(t, "link", l.ID, l.Label, links)
		if !boxes[l.From] || !boxes[l.To] || l.From == l.To {
			t.Errorf("link %s runs from %q to %q, want two boxes of the control plane", l.ID, l.From, l.To)
		}
		if strings.TrimSpace(l.Active) == "" && strings.TrimSpace(l.OK) == "" {
			t.Errorf("link %s needs an active or an ok expression", l.ID)
		}
		if l.ActiveText != "" && strings.TrimSpace(l.Active) == "" {
			t.Errorf("link %s has an activeText and no active expression", l.ID)
		}
	}

	for _, problem := range idleWordsWithoutNeeds(plane) {
		t.Error(problem)
	}
}

// idleWordsWithoutNeeds lists the entries of the control plane that would
// say something about the cluster with nothing behind it. An idle text such
// as "off", the word "off" of a link and the word "none" of an empty list
// need a needs expression: without one they would be said also while
// whatever reports the thing is down. The idle text "no data" claims
// nothing.
func idleWordsWithoutNeeds(plane controlPlaneSpec) []string {
	blank := func(expr string) bool { return strings.TrimSpace(expr) == "" }
	var problems []string
	claims := func(name string, s stateSpec) {
		if s.IdleText != "" && s.IdleText != "no data" && blank(s.Needs) {
			problems = append(problems, fmt.Sprintf("%s has the idle text %q and no needs expression to back it", name, s.IdleText))
		}
	}
	for _, b := range plane.Boxes {
		for _, s := range b.Stages {
			claims("box "+b.ID+", stage "+s.ID, s.stateSpec)
		}
		for _, r := range b.Rows {
			claims("box "+b.ID+", row "+r.ID, r.stateSpec)
		}
		if b.List != nil && blank(b.List.Needs) {
			problems = append(problems, fmt.Sprintf("box %s: its list says \"none\" and has no needs expression to back it", b.ID))
		}
	}
	for _, l := range plane.Links {
		if blank(l.Needs) {
			problems = append(problems, fmt.Sprintf("link %s says \"off\" and has no needs expression to back it", l.ID))
		}
	}
	return problems
}

// A copy of the control plane that drops the needs of one link or of the
// list fails the rule above: either would then say "off" or "none" while
// kube-state-metrics is down.
func TestIdleWordsWithoutNeedsAreCaught(t *testing.T) {
	shipped := loadDashboard(t).Topology.ControlPlane
	if problems := idleWordsWithoutNeeds(shipped); len(problems) > 0 {
		t.Fatalf("the shipped control plane: %v", problems)
	}
	// clone copies the plane through JSON, so that a change to the copy
	// leaves the shipped one alone.
	clone := func() controlPlaneSpec {
		raw, err := json.Marshal(shipped)
		if err != nil {
			t.Fatal(err)
		}
		var plane controlPlaneSpec
		if err := json.Unmarshal(raw, &plane); err != nil {
			t.Fatal(err)
		}
		return plane
	}
	drops := map[string]func(*controlPlaneSpec){}
	for at, l := range shipped.Links {
		drops["link "+l.ID] = func(p *controlPlaneSpec) { p.Links[at].Needs = "" }
	}
	for at, b := range shipped.Boxes {
		if b.List != nil {
			drops["list of "+b.ID] = func(p *controlPlaneSpec) { p.Boxes[at].List.Needs = "" }
		}
		for st, s := range b.Stages {
			if s.Needs != "" && s.IdleText != "" && s.IdleText != "no data" {
				drops["stage "+s.ID] = func(p *controlPlaneSpec) { p.Boxes[at].Stages[st].Needs = "" }
			}
		}
	}
	if len(drops) < 3 {
		t.Fatalf("%d entries to drop, want both links and the list at least", len(drops))
	}
	for name, drop := range drops {
		plane := clone()
		drop(&plane)
		if len(idleWordsWithoutNeeds(plane)) == 0 {
			t.Errorf("without the needs of %s the rule finds nothing", name)
		}
	}
}

// checkEntry checks the id and the label of a stage, a row or a link. seen
// holds the ids of its kind so far.
func checkEntry(t *testing.T, kind, id, label string, seen map[string]bool) {
	t.Helper()
	if id == "" || seen[id] {
		t.Errorf("%s id %q is empty or used twice", kind, id)
	}
	seen[id] = true
	if label == "" {
		t.Errorf("%s %s has no label", kind, id)
	}
}

// checkState checks that a stage or a row says how it gets its state: with
// up, or with at least one of failed, active, ok and set, never both. A text for
// a state the entry cannot be in is a mistake as well.
func checkState(t *testing.T, name string, s stateSpec) {
	t.Helper()
	asked := 0
	for _, expr := range []string{s.Failed, s.Active, s.OK, s.Set} {
		if strings.TrimSpace(expr) != "" {
			asked++
		}
	}
	short := strings.TrimSpace(s.Up) != ""
	switch {
	case short && asked > 0:
		t.Errorf("%s has up together with failed, active, ok or set: use one or the other", name)
	case !short && asked == 0:
		t.Errorf("%s needs up, or at least one of failed, active, ok and set", name)
	}
	if s.ActiveText != "" && strings.TrimSpace(s.Active) == "" {
		t.Errorf("%s has an activeText and no active expression", name)
	}
	if s.FailedText != "" && !short && strings.TrimSpace(s.Failed) == "" {
		t.Errorf("%s has a failedText and neither up nor a failed expression", name)
	}
}

// The slices of the Perses dashboard this test reads.
type persesDashboard struct {
	Spec struct {
		Duration        string                 `json:"duration"`
		RefreshInterval string                 `json:"refreshInterval"`
		Panels          map[string]persesPanel `json:"panels"`
		Layouts         []struct {
			Spec struct {
				Items []struct {
					X       int `json:"x"`
					Y       int `json:"y"`
					Width   int `json:"width"`
					Height  int `json:"height"`
					Content struct {
						Ref string `json:"$ref"`
					} `json:"content"`
				} `json:"items"`
			} `json:"spec"`
		} `json:"layouts"`
	} `json:"spec"`
}

type persesPanel struct {
	Spec struct {
		Display struct {
			Name string `json:"name"`
		} `json:"display"`
		Plugin struct {
			Kind string           `json:"kind"`
			Spec persesPluginSpec `json:"spec"`
		} `json:"plugin"`
		Queries []struct {
			Spec struct {
				Plugin struct {
					Spec struct {
						Query            string `json:"query"`
						SeriesNameFormat string `json:"seriesNameFormat"`
						Instant          bool   `json:"instant"`
					} `json:"spec"`
				} `json:"plugin"`
			} `json:"spec"`
		} `json:"queries"`
	} `json:"spec"`
}

type persesPluginSpec struct {
	Calculation   string            `json:"calculation"`
	Format        persesFormat      `json:"format"`
	LegendMode    string            `json:"legendMode"`
	Sparkline     *struct{}         `json:"sparkline"`
	Thresholds    *persesThresholds `json:"thresholds"`
	QuerySettings []struct {
		QueryIndex int          `json:"queryIndex"`
		Format     persesFormat `json:"format"`
	} `json:"querySettings"`
	YAxis struct {
		Format persesFormat `json:"format"`
		Min    *float64     `json:"min"`
		Max    *float64     `json:"max"`
	} `json:"yAxis"`
	Visual struct {
		Display      string `json:"display"`
		ConnectNulls bool   `json:"connectNulls"`
	} `json:"visual"`
	Legend *struct {
		Position string   `json:"position"`
		Values   []string `json:"values"`
	} `json:"legend"`
	ColumnSettings []struct {
		Name         string `json:"name"`
		Header       string `json:"header"`
		Width        int    `json:"width"`
		CellSettings []struct {
			Condition struct {
				Spec struct {
					Value string `json:"value"`
				} `json:"spec"`
			} `json:"condition"`
			Text      string `json:"text"`
			TextColor string `json:"textColor"`
		} `json:"cellSettings"`
	} `json:"columnSettings"`
}

type persesFormat struct {
	Unit          string `json:"unit"`
	DecimalPlaces *int   `json:"decimalPlaces"`
}

type persesThresholds struct {
	DefaultColor string `json:"defaultColor"`
	Steps        []struct {
		Value float64 `json:"value"`
		Color string  `json:"color"`
	} `json:"steps"`
}

var (
	persesKinds = map[string]string{"StatChart": "stat", "TimeSeriesChart": "timeseries", "Table": "table"}
	// The four colours the Perses dashboard uses, as tones.
	persesTones = map[string]string{"#73bf69": "good", "#fade2a": "warning", "#f2495c": "critical", "#5794f2": "info"}
	persesCalcs = map[string]string{"last-number": "last", "mean": "mean", "max": "max"}
)

func TestPanelsMatchPerses(t *testing.T) {
	d := loadDashboard(t)
	raw, err := os.ReadFile(persesPath)
	if err != nil {
		t.Fatalf("read %s: %v", persesPath, err)
	}
	var perses persesDashboard
	if err := json.Unmarshal(raw, &perses); err != nil {
		t.Fatalf("parse %s: %v", persesPath, err)
	}

	if d.Range != perses.Spec.Duration {
		t.Errorf("range = %q, Perses duration = %q", d.Range, perses.Spec.Duration)
	}
	if d.Refresh != perses.Spec.RefreshInterval {
		t.Errorf("refresh = %q, Perses refreshInterval = %q", d.Refresh, perses.Spec.RefreshInterval)
	}

	// Perses places panels through its layout; rows start below the Markdown
	// panel there and at 0 here.
	if len(perses.Spec.Layouts) != 1 {
		t.Fatalf("Perses dashboard has %d layouts, want 1", len(perses.Spec.Layouts))
	}
	wantGrid := map[string]gridSpec{}
	minY := -1
	for _, item := range perses.Spec.Layouts[0].Spec.Items {
		key := item.Content.Ref[strings.LastIndex(item.Content.Ref, "/")+1:]
		if key == persesTopologyPanel {
			continue
		}
		wantGrid[key] = gridSpec{X: item.X, Y: item.Y, W: item.Width, H: item.Height}
		if minY < 0 || item.Y < minY {
			minY = item.Y
		}
	}

	ours := map[string]panelSpec{}
	for _, p := range d.Panels {
		ours[p.ID] = p
		if _, ok := perses.Spec.Panels[p.ID]; !ok {
			t.Errorf("panel %q is not in the Perses dashboard", p.ID)
		}
	}

	for key, pp := range perses.Spec.Panels {
		if key == persesTopologyPanel {
			continue
		}
		t.Run(key, func(t *testing.T) {
			p, ok := ours[key]
			if !ok {
				t.Fatalf("Perses panel %q is missing from %s", key, dashboardPath)
			}
			spec := pp.Spec.Plugin.Spec
			equal(t, "title", p.Title, pp.Spec.Display.Name)
			equal(t, "kind", p.Kind, persesKinds[pp.Spec.Plugin.Kind])

			grid, placed := wantGrid[key]
			if !placed {
				t.Fatalf("Perses panel %q is in no layout", key)
			}
			grid.Y -= minY
			equal(t, "grid", p.Grid, grid)

			if len(p.Queries) != len(pp.Spec.Queries) {
				t.Fatalf("%d queries, Perses has %d", len(p.Queries), len(pp.Spec.Queries))
			}
			ownUnits := map[int]string{}
			for _, qs := range spec.QuerySettings {
				ownUnits[qs.QueryIndex] = qs.Format.Unit
			}
			for i, q := range p.Queries {
				want := pp.Spec.Queries[i].Spec.Plugin.Spec
				equal(t, fmt.Sprintf("queries[%d].expr", i), q.Expr, want.Query)
				equal(t, fmt.Sprintf("queries[%d].legend", i), q.Legend, want.SeriesNameFormat)
				equal(t, fmt.Sprintf("queries[%d].instant", i), q.Instant, want.Instant)
				equal(t, fmt.Sprintf("queries[%d].unit", i), q.Unit, ownUnits[i])
			}
			equal(t, "thresholds", tonesOf(p.Thresholds), persesTonesOf(t, spec.Thresholds))

			switch p.Kind {
			case "stat":
				equal(t, "unit", p.Unit, spec.Format.Unit)
				equal(t, "decimals", deref(p.Decimals), deref(spec.Format.DecimalPlaces))
				equal(t, "sparkline", p.Sparkline, spec.Sparkline != nil)
				equal(t, "label", p.Label, spec.LegendMode == "on")
				// A stat tile of the page shows the last value and nothing else.
				equal(t, "calculation", "last-number", spec.Calculation)
			case "timeseries":
				equal(t, "unit", p.Unit, spec.YAxis.Format.Unit)
				equal(t, "min", deref(p.Min), deref(spec.YAxis.Min))
				equal(t, "max", deref(p.Max), deref(spec.YAxis.Max))
				equal(t, "draw", p.Draw, map[string]string{"line": "line", "bar": "bars"}[spec.Visual.Display])
				equal(t, "connectNulls", p.ConnectNulls, spec.Visual.ConnectNulls)
				equal(t, "legend", legendOf(p.Legend), persesLegendOf(t, spec))
			case "table":
				if len(p.Columns) != len(spec.ColumnSettings) {
					t.Fatalf("%d columns, Perses has %d", len(p.Columns), len(spec.ColumnSettings))
				}
				for i, c := range p.Columns {
					want := spec.ColumnSettings[i]
					equal(t, fmt.Sprintf("columns[%d].label", i), c.Label, want.Name)
					equal(t, fmt.Sprintf("columns[%d].header", i), c.Header, want.Header)
					equal(t, fmt.Sprintf("columns[%d].width", i), c.Width, want.Width)
					if len(c.Map) != len(want.CellSettings) {
						t.Fatalf("column %q maps %d values, Perses maps %d", c.Label, len(c.Map), len(want.CellSettings))
					}
					for j, m := range c.Map {
						cell := want.CellSettings[j]
						equal(t, fmt.Sprintf("columns[%d].map[%d].match", i, j), m.Match, cell.Condition.Spec.Value)
						equal(t, fmt.Sprintf("columns[%d].map[%d].tone", i, j), m.Tone, persesTones[cell.TextColor])
						// Perses prefixes the text with a symbol; here the tone dot does that job.
						if !strings.HasSuffix(cell.Text, m.Text) {
							t.Errorf("columns[%d].map[%d].text = %q, Perses shows %q", i, j, m.Text, cell.Text)
						}
					}
				}
			}
		})
	}
}

func equal[T any](t *testing.T, field string, got, want T) {
	t.Helper()
	if g, w := fmt.Sprintf("%+v", got), fmt.Sprintf("%+v", want); g != w {
		t.Errorf("%s = %s, Perses has %s", field, g, w)
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func tonesOf(th *thresholdSpec) string {
	if th == nil {
		return ""
	}
	parts := []string{th.Base}
	for _, s := range th.Steps {
		parts = append(parts, fmt.Sprintf("%v:%s", s.From, s.Tone))
	}
	return strings.Join(parts, " ")
}

func persesTonesOf(t *testing.T, th *persesThresholds) string {
	t.Helper()
	if th == nil {
		return ""
	}
	tone := func(color string) string {
		name, ok := persesTones[color]
		if !ok {
			t.Errorf("Perses threshold colour %q has no tone; add it to persesTones", color)
		}
		return name
	}
	parts := []string{tone(th.DefaultColor)}
	for _, s := range th.Steps {
		parts = append(parts, fmt.Sprintf("%v:%s", s.Value, tone(s.Color)))
	}
	return strings.Join(parts, " ")
}

func legendOf(l *legendSpec) string {
	if l == nil {
		return ""
	}
	return l.Position + " " + strings.Join(l.Values, ",")
}

func persesLegendOf(t *testing.T, spec persesPluginSpec) string {
	t.Helper()
	if spec.Legend == nil {
		return ""
	}
	values := make([]string, 0, len(spec.Legend.Values))
	for _, v := range spec.Legend.Values {
		calc, ok := persesCalcs[v]
		if !ok {
			t.Errorf("Perses legend value %q has no counterpart; add it to persesCalcs", v)
		}
		values = append(values, calc)
	}
	return spec.Legend.Position + " " + strings.Join(values, ",")
}
