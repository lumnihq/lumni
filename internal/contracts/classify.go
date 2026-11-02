package contracts

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

type Rank struct {
	Type string  `json:"type"`
	P    float64 `json:"p"`
}
type Cue struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
}
type Reading struct {
	Text    string         `json:"text"`
	Ranked  []Rank         `json:"ranked"`
	State   string         `json:"state"`
	Type    *string        `json:"type"`
	Slots   map[string]any `json:"slots"`
	Guessed []string       `json:"guessed"`
	Cues    []Cue          `json:"cues"`
	Source  string         `json:"source"`
}

var cues = map[string][]string{
	"once-per-key":  {`(?i)\b(no more than|at most|only) (one|1)\b`, `(?i)\b(twice|duplicate|second|once)\b`},
	"approval-over": {`(?i)\b(ask|approv\w*|person|human|manager)\b`, `(?i)(over|above|greater than)\s*\$?\d`},
	"end-state":     {`(?i)\b(must leave|must be|verify|check|inactive|canceled|closed|paid)\b`},
	"amount-cap":    {`(?i)(eligible balance|amount paid|order total|daily budget|remaining balance)`, `(?i)(not exceed|never exceed|within|≤|<=)`},
	"order-before":  {`(?i)\b(before|only after|until)\b`, `(?i)\b(lands?|landed|settled|finished)\b`},
	"rate-limit":    {`(?i)\b(per|each|a|every) (minute|hour|day|week)\b`, `(?i)(at most|no more than) [2-9]`},
	"human-wins":    {`(?i)\b(overwrite|edited|edit|person edited|human edited)\b`, `(?i)\blast \d+ minutes?\b`},
}

func Classify(text string) []Reading {
	out := []Reading{}
	for _, sentence := range regexp.MustCompile(`[.!?]\s+|[;\n]+`).Split(text, -1) {
		sentence = strings.TrimSpace(strings.TrimRight(sentence, ".!?"))
		if sentence == "" {
			continue
		}
		r := Reading{Text: sentence, Ranked: []Rank{}, State: "pick", Slots: map[string]any{}, Guessed: []string{}, Cues: []Cue{}, Source: "deterministic"}
		weights := map[string]float64{}
		sum := 1.0
		for _, kind := range Kinds {
			score := 0.0
			for _, cue := range cues[kind] {
				loc := regexp.MustCompile(cue).FindStringIndex(sentence)
				if loc != nil {
					score += 3
					r.Cues = append(r.Cues, Cue{len(utf16.Encode([]rune(sentence[:loc[0]]))), len(utf16.Encode([]rune(sentence[:loc[1]]))), kind})
				}
			}
			weights[kind] = math.Exp(score)
			sum += weights[kind]
		}
		for _, kind := range Kinds {
			r.Ranked = append(r.Ranked, Rank{kind, weights[kind] / sum})
		}
		sort.SliceStable(r.Ranked, func(i, j int) bool { return r.Ranked[i].P > r.Ranked[j].P })
		if r.Ranked[0].P > .6 && r.Ranked[0].P-r.Ranked[1].P > .3 {
			r.State = "sure"
			kind := r.Ranked[0].Type
			r.Type = &kind
		} else if r.Ranked[0].P > .2 {
			r.State = "ask"
		}
		low := strings.ToLower(sentence)
		action := "refund"
		app := "stripe"
		key := "order"
		for _, a := range []struct{ words, action, app, key string }{{"cancel", "cancellation", "stripe", "subscription"}, {"email", "email", "gmail", "customer"}, {"ticket", "ticket close", "zendesk", "ticket"}, {"deploy", "deploy", "vercel", "project"}, {"record", "record update", "salesforce", "record"}} {
			if strings.Contains(low, a.words) {
				action, app, key = a.action, a.app, a.key
				break
			}
		}
		if strings.Contains(low, "refund") {
			action = "refund"
			app = "stripe"
			key = "order"
		}
		r.Slots["action"] = action
		r.Slots["app"] = app
		r.Slots["key"] = key
		explicitApp := false
		for _, name := range []string{"stripe", "shopify", "salesforce", "zendesk", "gmail", "quickbooks", "vercel", "hubspot"} {
			if strings.Contains(low, name) {
				r.Slots["app"] = name
				explicitApp = true
			}
		}
		if !explicitApp {
			r.Guessed = append(r.Guessed, "app")
		}
		keyMatch := regexp.MustCompile(`(?i)(?:per|same|each) (order|customer|subscription|ticket|record|project|recipient|invoice)`).FindStringSubmatch(sentence)
		if len(keyMatch) > 0 {
			r.Slots["key"] = strings.ToLower(keyMatch[1])
		} else {
			r.Guessed = append(r.Guessed, "key")
		}
		money := regexp.MustCompile(`\$\s*([0-9,]+(?:\.[0-9]{1,2})?)`).FindStringSubmatch(sentence)
		if len(money) > 0 {
			n, _ := strconv.ParseFloat(strings.ReplaceAll(money[1], ",", ""), 64)
			r.Slots["amount"] = n
			r.Slots["amount_minor"] = math.Round(n * 100)
		} else {
			r.Guessed = append(r.Guessed, "amount_minor")
		}
		for _, state := range []string{"inactive", "canceled", "closed", "paid", "active", "succeeded"} {
			if strings.Contains(low, state) {
				r.Slots["state"] = state
				break
			}
		}
		for _, obj := range []string{"subscription", "refund", "invoice", "order", "ticket", "record"} {
			if strings.Contains(low, obj) {
				r.Slots["object"] = obj
				break
			}
		}
		m := regexp.MustCompile(`(?i)(?:no more than|at most) (\d+)`).FindStringSubmatch(sentence)
		if len(m) > 0 {
			n, _ := strconv.Atoi(m[1])
			r.Slots["n"] = n
		}
		for _, w := range []string{"minute", "hour", "day", "week"} {
			if strings.Contains(low, " "+w) {
				r.Slots["window"] = w
				break
			}
		}
		m = regexp.MustCompile(`(?i)(\d+) minutes?`).FindStringSubmatch(sentence)
		if len(m) > 0 {
			n, _ := strconv.Atoi(m[1])
			r.Slots["minutes"] = n
		}
		for _, limit := range []string{"eligible balance", "amount paid", "order total", "daily budget", "remaining balance"} {
			if strings.Contains(low, limit) {
				r.Slots["limit"] = limit
			}
		}
		r.Guessed = append(r.Guessed, "execution_binding")
		out = append(out, r)
	}
	return out
}
func ApplyRanking(r *Reading, probabilities map[string]float64) {
	ranked := []Rank{}
	sum := 0.0
	for _, kind := range Kinds {
		p := probabilities[kind]
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return
		}
		sum += p
		ranked = append(ranked, Rank{kind, p})
	}
	if sum <= 0 || sum > 1.000001 {
		return
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].P > ranked[j].P })
	r.Ranked = ranked
	r.Type = nil
	r.State = "pick"
	if ranked[0].P >= .6 && ranked[0].P-ranked[1].P >= .3 {
		r.State = "sure"
		kind := ranked[0].Type
		r.Type = &kind
	} else if ranked[0].P >= .2 {
		r.State = "ask"
	}
	r.Source = "jev"
}
