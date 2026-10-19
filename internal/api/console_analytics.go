package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"lumni/internal/analytics"
)

func timeRange(r *http.Request, max time.Duration) (time.Time, time.Time, *time.Location, error) {
	z := r.URL.Query().Get("tz")
	if z == "" {
		z = "UTC"
	}
	loc, e := time.LoadLocation(z)
	if e != nil {
		return time.Time{}, time.Time{}, nil, errors.New("tz must be an IANA zone")
	}
	to := time.Now().UTC()
	local := to.In(loc)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
	for _, k := range []string{"from", "to"} {
		if v := r.URL.Query().Get(k); v != "" {
			t, e := time.Parse(time.RFC3339Nano, v)
			if e != nil {
				return from, to, loc, errors.New(k + " must be RFC 3339")
			}
			if k == "from" {
				from = t.UTC()
			} else {
				to = t.UTC()
			}
		}
	}
	if !from.Before(to) || to.Sub(from) > max {
		return from, to, loc, fmt.Errorf("range must be positive and at most %s", max)
	}
	return from, to, loc, nil
}
func (s *Server) consoleMetrics(r *http.Request, from, to time.Time) ([]analytics.ConsoleMetric, error) {
	if a, ok := s.analytics.(analytics.ConsoleStore); ok {
		return a.Console(r.Context(), tenant(r), from, to)
	}
	return analytics.NewPostgres(s.pool).Console(r.Context(), tenant(r), from, to)
}

type counts struct {
	Total    int64            `json:"total"`
	Kinds    map[string]int64 `json:"kinds"`
	Would    map[string]int64 `json:"would"`
	Outcomes map[string]int64 `json:"outcomes"`
	Cost     int64            `json:"cost_micros_usd"`
	Kept     int64            `json:"kept_micros_usd"`
	Verified int64            `json:"verified"`
}

func newCounts() *counts {
	return &counts{Kinds: map[string]int64{"allow": 0, "stop": 0, "wait": 0, "recheck": 0, "ask": 0, "bad": 0, "blind": 0}, Would: map[string]int64{"stop": 0, "wait": 0, "recheck": 0, "ask": 0}, Outcomes: map[string]int64{"VERIFIED": 0, "MISMATCH": 0, "PARTIAL": 0, "FAILED": 0, "PENDING": 0, "UNKNOWN": 0}}
}
func effectiveKind(m analytics.ConsoleMetric) string {
	if m.Kind == "allow" && (m.Outcome == "MISMATCH" || m.Outcome == "PARTIAL" || m.Outcome == "FAILED") {
		return "bad"
	}
	return m.Kind
}
func (c *counts) add(m analytics.ConsoleMetric) {
	c.Total += m.Total
	c.Cost += m.Cost
	c.Kept += m.Kept
	k := effectiveKind(m)
	c.Kinds[k] += m.Total
	if m.Outcome == "UNKNOWN" {
		c.Kinds["blind"] += m.Total
	}
	if m.Would != "" {
		c.Would[m.Would] += m.Total
	}
	c.Outcomes[m.Outcome] += m.Total
	if m.Outcome == "VERIFIED" {
		c.Verified += m.Total
	}
}

type breakdown struct {
	*counts
	Actor       string    `json:"actor_id,omitempty"`
	App         string    `json:"app,omitempty"`
	Operation   string    `json:"operation,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Reason      string    `json:"reason_code,omitempty"`
	Count       int64     `json:"count,omitempty"`
	Hours       []int64   `json:"hours,omitempty"`
	Last        time.Time `json:"last_at"`
	Streak      int64     `json:"streak"`
	Checked     bool      `json:"checked"`
	Sample      string    `json:"sample_action_id,omitempty"`
	TopResource string    `json:"top_resource,omitempty"`
}

func topBreakdowns(m map[string]*breakdown, n int) []*breakdown {
	out := []*breakdown{}
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total == out[j].Total {
			return out[i].Actor+out[i].App+out[i].Operation+out[i].Reason < out[j].Actor+out[j].App+out[j].Operation+out[j].Reason
		}
		return out[i].Total > out[j].Total
	})
	if len(out) > n {
		other := &breakdown{counts: newCounts(), Actor: "other", App: "other", Operation: "other", Reason: "other", Hours: make([]int64, 24)}
		for _, v := range out[n:] {
			other.Total += v.Total
			other.Count += v.Count
			other.Cost += v.Cost
			other.Kept += v.Kept
			other.Verified += v.Verified
			for k, x := range v.Kinds {
				other.Kinds[k] += x
			}
			for k, x := range v.Would {
				other.Would[k] += x
			}
			for k, x := range v.Outcomes {
				other.Outcomes[k] += x
			}
			if v.Last.After(other.Last) {
				other.Last = v.Last
			}
			for h, x := range v.Hours {
				other.Hours[h] += x
			}
		}
		out = append(out[:n], other)
	}
	return out
}
func (s *Server) analyticsDay(w http.ResponseWriter, r *http.Request) {
	from, to, z, e := timeRange(r, 7*24*time.Hour)
	if e != nil {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", e.Error(), nil)
		return
	}
	top := 10
	if v := r.URL.Query().Get("top"); v != "" {
		top, e = strconv.Atoi(v)
		if e != nil || top < 1 || top > 50 {
			s.fail(w, r, 400, "INVALID_QUERY", "top must be 1–50", nil)
			return
		}
	}
	groups := csv(r.URL.Query().Get("group_by"))
	if len(groups) == 0 {
		groups = []string{"kind", "hour", "actor", "app", "reason"}
	}
	for _, g := range groups {
		if !contains([]string{"kind", "hour", "actor", "app", "reason", "resource", "operation"}, g) {
			s.fail(w, r, 400, "INVALID_GROUP_BY", "Unsupported analytics dimension", nil)
			return
		}
	}
	metrics, err := s.consoleMetrics(r, from, to)
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	total := newCounts()
	hours := make([]*counts, 24)
	for i := range hours {
		hours[i] = newCounts()
	}
	actors, apps, reasons, operations := map[string]*breakdown{}, map[string]*breakdown{}, map[string]*breakdown{}, map[string]*breakdown{}
	actorOperations := map[string]map[string]int64{}
	actorApps := map[string]string{}
	add := func(m map[string]*breakdown, key string, row analytics.ConsoleMetric) *breakdown {
		b := m[key]
		if b == nil {
			b = &breakdown{counts: newCounts(), Hours: make([]int64, 24)}
			m[key] = b
		}
		b.add(row)
		h := row.Bucket.In(z).Hour()
		b.Hours[h] += row.Total
		if row.Last.After(b.Last) {
			b.Last = row.Last
		}
		return b
	}
	for _, m := range metrics {
		total.add(m)
		h := m.Bucket.In(z).Hour()
		hours[h].add(m)
		a := add(actors, m.Actor, m)
		a.Actor = m.Actor
		a.App = m.App
		if actorOperations[m.Actor] == nil {
			actorOperations[m.Actor] = map[string]int64{}
		}
		actorOperations[m.Actor][m.Operation] += m.Total
		actorApps[m.Actor+"|"+m.Operation] = m.App
		op := add(operations, m.Operation, m)
		op.Operation = m.Operation
		b := add(apps, m.App, m)
		b.App = m.App
		k := effectiveKind(m)
		if k != "allow" {
			key := k + "|" + m.Reason + "|" + m.Operation + "|" + m.Actor + "|" + m.App
			b = add(reasons, key, m)
			b.Kind = k
			b.Reason = m.Reason
			b.Actor = m.Actor
			b.Operation = m.Operation
			b.App = m.App
			b.Count = b.Total
		}
	}
	costs, e := analytics.NewPostgres(s.pool).ConsoleCosts(r.Context(), tenant(r), from, to)
	if e != nil {
		s.respond(w, r, 0, nil, e)
		return
	}
	total.Cost = 0
	for _, group := range []map[string]*breakdown{actors, apps, operations} {
		for _, b := range group {
			b.Cost = 0
		}
	}
	for _, c := range costs {
		total.Cost += c.Cost
		if b := actors[c.Actor]; b != nil {
			b.Cost += c.Cost
		}
		if b := apps[c.App]; b != nil {
			b.Cost += c.Cost
		}
		if b := operations[c.Operation]; b != nil {
			b.Cost += c.Cost
		}
	}
	for _, actor := range actors {
		var best int64
		for op, n := range actorOperations[actor.Actor] {
			if n > best {
				best = n
				actor.Operation = op
				actor.App = actorApps[actor.Actor+"|"+op]
			}
		}
	}
	out := map[string]any{"from": from, "to": to, "tz": z.String(), "total": total.Total, "kinds": total.Kinds, "would": total.Would, "outcomes": total.Outcomes, "cost_micros_usd": total.Cost, "kept_micros_usd": total.Kept, "truncated": false, "actor_count": len(actors), "app_count": len(apps)}
	if contains(groups, "hour") {
		hs := []map[string]any{}
		for h, c := range hours {
			hs = append(hs, map[string]any{"h": h, "total": c.Total, "kinds": c.Kinds})
		}
		out["hours"] = hs
	}
	if contains(groups, "actor") {
		xs := topBreakdowns(actors, top)
		for _, a := range xs {
			if a.Actor == "other" {
				continue
			}
			a.Streak, err = s.actorStreak(r, a.Actor, from, to, metrics)
			if err != nil {
				s.respond(w, r, 0, nil, err)
				return
			}
		}
		out["by_actor"] = xs
	}
	if contains(groups, "app") {
		xs := topBreakdowns(apps, top)
		rows, e := s.pool.Query(r.Context(), `SELECT lower(kind),COALESCE(config->>'app','') FROM verifiers WHERE tenant_id=$1 AND enabled`, tenant(r))
		if e != nil {
			s.respond(w, r, 0, nil, e)
			return
		}
		checked := map[string]bool{}
		for rows.Next() {
			var k, a string
			if e = rows.Scan(&k, &a); e != nil {
				break
			}
			checked[k] = true
			checked[a] = true
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			s.respond(w, r, 0, nil, e)
			return
		}
		for _, x := range xs {
			x.Checked = checked[x.App]
		}
		out["by_app"] = xs
	}
	if contains(groups, "reason") {
		xs := topBreakdowns(reasons, top)
		start, end := analytics.ConsoleCellBounds(from, to)
		for _, b := range xs {
			if b.Reason == "other" {
				continue
			}
			// The sample stops at the first indexed fact. Resource counts use
			// corrected aggregates, reading facts only at partial boundaries.
			err = s.pool.QueryRow(r.Context(), `WITH matching AS NOT MATERIALIZED (
 SELECT action_id,at,resources FROM console_action_facts WHERE tenant_id=$1 AND actor_id=$2 AND operation=$3 AND reason_code=$4 AND at>=$5 AND at<$6 AND app=$7 AND CASE WHEN kind='allow' AND outcome IN('MISMATCH','PARTIAL','FAILED') THEN 'bad' ELSE kind END=$8
), touches AS (
 SELECT resource_id,total FROM console_reason_resource_metrics WHERE tenant_id=$1 AND actor_id=$2 AND operation=$3 AND reason_code=$4 AND app=$7 AND kind=$8 AND bucket>=$9 AND bucket<$10
 UNION ALL SELECT unnest(resources),1 FROM matching WHERE at<LEAST($6::timestamptz,$9::timestamptz)
 UNION ALL SELECT unnest(resources),1 FROM matching WHERE at>=GREATEST($5::timestamptz,$9::timestamptz,$10::timestamptz)
)
 SELECT (SELECT action_id FROM matching ORDER BY at DESC,action_id DESC LIMIT 1),COALESCE((SELECT resource_id FROM touches GROUP BY resource_id HAVING sum(total)>0 ORDER BY sum(total) DESC,resource_id LIMIT 1),'')`, tenant(r), b.Actor, b.Operation, b.Reason, from, to, b.App, b.Kind, start, end).Scan(&b.Sample, &b.TopResource)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				s.respond(w, r, 0, nil, err)
				return
			}
		}
		out["by_reason"] = xs
	}
	if contains(groups, "operation") {
		out["by_operation"] = topBreakdowns(operations, top)
	}
	if contains(groups, "resource") {
		rs, e := s.resourceAnalytics(r, from, to, z, top)
		if e != nil {
			s.respond(w, r, 0, nil, e)
			return
		}
		out["resource_count"] = int64(0)
		if len(rs) > 0 {
			out["resource_count"] = rs[0]["_resource_count"]
		}
		for _, row := range rs {
			delete(row, "_resource_count")
		}
		out["by_resource"] = rs
	}
	writeJSON(w, 200, out)
}
func (s *Server) resourceAnalytics(r *http.Request, from, to time.Time, z *time.Location, top int) ([]map[string]any, error) {
	start, end := analytics.ConsoleCellBounds(from, to)
	rows, err := s.pool.Query(r.Context(), `WITH raw AS(
 SELECT bucket,resources,actor_id,kind FROM console_action_facts WHERE tenant_id=$1 AND at>=$2 AND at<LEAST($3::timestamptz,$5::timestamptz)
 UNION ALL SELECT bucket,resources,actor_id,kind FROM console_action_facts WHERE tenant_id=$1 AND at>=GREATEST($2::timestamptz,$5::timestamptz,$6::timestamptz) AND at<$3),
 cells AS (
 SELECT bucket,resource_id,actor_id,kind,total FROM console_resource_metrics WHERE tenant_id=$1 AND bucket>=$5 AND bucket<$6
 UNION ALL SELECT bucket,unnest(resources),actor_id,kind,1 FROM raw),
 ranked AS(SELECT resource_id,row_number()OVER(ORDER BY sum(total)DESC,resource_id)rank,count(*)OVER()resource_count FROM cells GROUP BY resource_id HAVING sum(total)>0),
 mapped AS(SELECT c.bucket,c.actor_id,c.kind,c.total,CASE WHEN r.rank<=$4 THEN c.resource_id ELSE 'other' END resource_id,r.resource_count FROM cells c JOIN ranked r USING(resource_id)),
 stats AS(SELECT resource_id,sum(total)touches,count(DISTINCT actor_id)FILTER(WHERE total>0)actors,max(resource_count)resource_count FROM mapped GROUP BY resource_id),
 kinds AS(SELECT resource_id,kind,sum(total)n FROM mapped GROUP BY resource_id,kind)
 SELECT s.resource_id,COALESCE(r.metadata->>'name',r.external_id,s.resource_id),s.touches,s.actors,
 COALESCE((SELECT jsonb_object_agg(kind,n) FROM kinds k WHERE k.resource_id=s.resource_id),'{}'),s.resource_count
 FROM stats s LEFT JOIN resources r ON r.tenant_id=$1 AND r.id=s.resource_id ORDER BY (s.resource_id='other'),s.touches DESC,s.resource_id`, tenant(r), from, to, top, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name string
		var total, actors, resourceCount int64
		var kinds []byte
		if err = rows.Scan(&id, &name, &total, &actors, &kinds, &resourceCount); err != nil {
			return nil, err
		}
		var k map[string]int64
		_ = json.Unmarshal(kinds, &k)
		out = append(out, map[string]any{"_resource_count": resourceCount, "resource_id": id, "name": name, "touches": total, "actors": actors, "stepins": map[string]int64{"stop": k["stop"], "wait": k["wait"], "recheck": k["recheck"], "ask": k["ask"]}, "hours": map[string]any{}})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	ids := []string{}
	byID := map[string]map[string]any{}
	actorsByID := map[string]map[string]int64{}
	for _, item := range out {
		id := item["resource_id"].(string)
		ids = append(ids, id)
		byID[id] = item
		actorsByID[id] = map[string]int64{}
		item["hours"] = map[string][]int64{"stop": make([]int64, 24), "wait": make([]int64, 24), "recheck": make([]int64, 24), "ask": make([]int64, 24)}
	}
	if len(ids) == 0 {
		return out, nil
	}
	hourRows, e := s.pool.Query(r.Context(), `WITH raw AS(
 SELECT bucket,resources,actor_id,kind FROM console_action_facts WHERE tenant_id=$1 AND at>=$2 AND at<LEAST($3::timestamptz,$6::timestamptz)
 UNION ALL SELECT bucket,resources,actor_id,kind FROM console_action_facts WHERE tenant_id=$1 AND at>=GREATEST($2::timestamptz,$6::timestamptz,$7::timestamptz) AND at<$3),
 cells AS(
 SELECT bucket,CASE WHEN resource_id=ANY($4) THEN resource_id ELSE 'other' END resource_id,actor_id,kind,total FROM console_resource_metrics WHERE tenant_id=$1 AND (resource_id=ANY($4) OR 'other'=ANY($4)) AND bucket>=$6 AND bucket<$7
 UNION ALL SELECT bucket,CASE WHEN r=ANY($4) THEN r ELSE 'other' END,actor_id,kind,1 FROM raw CROSS JOIN LATERAL unnest(resources)r WHERE r=ANY($4) OR 'other'=ANY($4))
 SELECT resource_id,actor_id,kind,extract(hour FROM bucket AT TIME ZONE $5)::int,sum(total) FROM cells GROUP BY 1,2,3,4 HAVING sum(total)>0`, tenant(r), from, to, ids, z.String(), start, end)
	if e != nil {
		return nil, e
	}
	defer hourRows.Close()
	for hourRows.Next() {
		var id, actor, kind string
		var hour int
		var total int64
		if e = hourRows.Scan(&id, &actor, &kind, &hour, &total); e != nil {
			return nil, e
		}
		item := byID[id]
		if item == nil {
			continue
		}
		hours := item["hours"].(map[string][]int64)
		if values, ok := hours[kind]; ok {
			values[hour] += total
		}
		actorsByID[id][actor] += total
	}
	for id, item := range byID {
		actors := []map[string]any{}
		for actor, n := range actorsByID[id] {
			actors = append(actors, map[string]any{"actor_id": actor, "touches": n})
		}
		sort.Slice(actors, func(i, j int) bool { return actors[i]["touches"].(int64) > actors[j]["touches"].(int64) })
		if len(actors) > 8 {
			actors = actors[:8]
		}
		item["by_actor"] = actors
	}
	return out, hourRows.Err()
}
func (s *Server) analyticsDaily(w http.ResponseWriter, r *http.Request) {
	days := 30
	var e error
	if v := r.URL.Query().Get("days"); v != "" {
		days, e = strconv.Atoi(v)
		if e != nil || days < 1 || days > 90 {
			s.fail(w, r, 400, "INVALID_QUERY", "days must be 1–90", nil)
			return
		}
	}
	hour := -1
	if v := r.URL.Query().Get("at_hour"); v != "" {
		hour, e = strconv.Atoi(v)
		if e != nil || hour < 0 || hour > 23 {
			s.fail(w, r, 400, "INVALID_QUERY", "at_hour must be 0–23", nil)
			return
		}
	}
	_, _, z, e := timeRange(r, 90*24*time.Hour)
	if e != nil {
		s.fail(w, r, 400, "INVALID_TIME_RANGE", e.Error(), nil)
		return
	}
	now := time.Now().In(z)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, z).AddDate(0, 0, -days+1)
	metrics, err := s.consoleMetrics(r, start.UTC(), time.Now().UTC())
	if err != nil {
		s.respond(w, r, 0, nil, err)
		return
	}
	all, until := map[string]*counts{}, map[string]*counts{}
	for _, m := range metrics {
		date := m.Bucket.In(z).Format("2006-01-02")
		if all[date] == nil {
			all[date] = newCounts()
			until[date] = newCounts()
		}
		all[date].add(m)
		if hour < 0 || m.Bucket.In(z).Hour() <= hour {
			until[date].add(m)
		}
	}
	costs, e := analytics.NewPostgres(s.pool).ConsoleCosts(r.Context(), tenant(r), start.UTC(), time.Now().UTC())
	if e != nil {
		s.respond(w, r, 0, nil, e)
		return
	}
	for _, c := range all {
		c.Cost = 0
	}
	for _, c := range costs {
		date := c.Bucket.In(z).Format("2006-01-02")
		if all[date] == nil {
			all[date] = newCounts()
			until[date] = newCounts()
		}
		all[date].Cost += c.Cost
	}
	out := []map[string]any{}
	for d := 0; d < days; d++ {
		date := start.AddDate(0, 0, d).Format("2006-01-02")
		c := all[date]
		if c == nil {
			c = newCounts()
			until[date] = newCounts()
		}
		out = append(out, map[string]any{"date": date, "total": c.Total, "kinds": c.Kinds, "kept_micros_usd": c.Kept, "cost_micros_usd": c.Cost, "verified": c.Verified, "until_hour": map[string]any{"total": until[date].Total}})
	}
	writeJSON(w, 200, map[string]any{"days": out})
}
