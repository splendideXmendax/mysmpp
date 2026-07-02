package filter

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type Action string

const (
	ActionPass  Action = "pass"
	ActionBlock Action = "block"
	ActionMask  Action = "mask"
)

type Decision struct {
	Action  Action
	Reason  string
	NewText string
	Tags    map[string]struct{}
}

type Engine struct {
	normalize config.NormalizeConfig
	rules     []compiledRule
	ac        *acMatcher
	regexps   []compiledRegex
}

type compiledRule struct {
	name     string
	action   string
	tag      string
	maskWith string
	priority int
}

type compiledRegex struct {
	rule int
	re   *regexp.Regexp
}

func Compile(cfg config.FilterConfig) (*Engine, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	e := &Engine{normalize: cfg.Normalize}
	patterns := []pattern{}
	for _, rule := range cfg.Rules {
		if rule.Enabled != nil && !*rule.Enabled {
			continue
		}
		idx := len(e.rules)
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		if action == "" {
			action = string(ActionPass)
		}
		maskWith := rule.MaskWith
		if maskWith == "" {
			maskWith = "*"
		}
		e.rules = append(e.rules, compiledRule{
			name:     rule.Name,
			action:   action,
			tag:      rule.Tag,
			maskWith: maskWith,
			priority: rule.Priority,
		})
		for _, kw := range rule.Keywords {
			kw = normalizeText(kw, cfg.Normalize)
			if kw != "" {
				patterns = append(patterns, pattern{text: kw, rule: idx})
			}
		}
		if rule.Regex != "" {
			re, err := regexp.Compile(rule.Regex)
			if err != nil {
				return nil, err
			}
			e.regexps = append(e.regexps, compiledRegex{rule: idx, re: re})
		}
	}
	e.ac = buildAC(patterns)
	return e, nil
}

func (e *Engine) Evaluate(text string) Decision {
	if e == nil {
		return Decision{Action: ActionPass, NewText: text}
	}
	normalized, normToOrig := normalizeTextWithMap(text, e.normalize)
	decision := Decision{Action: ActionPass, NewText: text, Tags: map[string]struct{}{}}
	hits := []ruleHit{}
	for _, m := range e.ac.Find(normalized) {
		hits = append(hits, ruleHit{rule: m.Rule, start: m.Start, end: m.End, text: m.Text})
	}
	for _, cr := range e.regexps {
		for _, loc := range cr.re.FindAllStringIndex(normalized, -1) {
			hits = append(hits, ruleHit{rule: cr.rule, start: loc[0], end: loc[1]})
		}
	}
	if len(hits) == 0 {
		return decision
	}
	sort.SliceStable(hits, func(i, j int) bool {
		ri := e.rules[hits[i].rule]
		rj := e.rules[hits[j].rule]
		if actionRank(ri.action) != actionRank(rj.action) {
			return actionRank(ri.action) > actionRank(rj.action)
		}
		return ri.priority > rj.priority
	})
	for _, hit := range hits {
		rule := e.rules[hit.rule]
		if rule.action == "tag" && rule.tag != "" {
			decision.Tags[rule.tag] = struct{}{}
		}
	}
	for _, hit := range hits {
		rule := e.rules[hit.rule]
		switch rule.action {
		case "block":
			decision.Action = ActionBlock
			decision.Reason = rule.name
			return decision
		case "mask":
			decision.Action = ActionMask
			decision.Reason = rule.name
			decision.NewText = maskText(text, collectMaskHits(e, hits, rule.name, normToOrig))
			return decision
		}
	}
	return decision
}

func TextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

type ruleHit struct {
	rule       int
	start, end int
	text       string
}

type maskInterval struct {
	start, end int
	with       string
}

func collectMaskHits(e *Engine, hits []ruleHit, ruleName string, normToOrig []int) []maskInterval {
	intervals := []maskInterval{}
	for _, hit := range hits {
		rule := e.rules[hit.rule]
		if rule.action == "mask" && rule.name == ruleName && hit.start >= 0 && hit.end >= hit.start {
			start, end := hit.start, hit.end
			if start < len(normToOrig) && end < len(normToOrig) {
				start = normToOrig[start]
				end = normToOrig[end]
			}
			intervals = append(intervals, maskInterval{start: start, end: end, with: rule.maskWith})
		}
	}
	return intervals
}

func maskText(text string, intervals []maskInterval) string {
	if len(intervals) == 0 {
		return text
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	var b strings.Builder
	pos := 0
	for _, in := range intervals {
		if in.start < pos {
			continue
		}
		if in.start > len(text) || in.end > len(text) {
			continue
		}
		b.WriteString(text[pos:in.start])
		b.WriteString(in.with)
		pos = in.end
	}
	b.WriteString(text[pos:])
	return b.String()
}

func actionRank(action string) int {
	switch action {
	case "block":
		return 4
	case "mask":
		return 3
	case "tag":
		return 2
	default:
		return 1
	}
}
