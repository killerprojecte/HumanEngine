// Package memorylexical implements source query tokenization with pinned Node ICU.
package memorylexical

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"strings"
)

func Lower(s string) (string, error) { return jsstring.Lower(s) }

var generic = func() map[string]bool {
	m := map[string]bool{}
	for _, v := range strings.Fields("什么 怎么 如何 是否 能否 请问 一下 一个 这个 那个 哪些 这样 那样 之前 现在 以后 曾经 记得 记住 知道 事情 东西 内容 问题 信息 有关 相关 说过 过的 应该 可以 需要 不是 没有 不要 还有 以及 因为 所以 但是 然后 情况 时候 用户 我们 你们 他们 她们 大家") {
		m[v] = true
	}
	return m
}()

func Terms(query string) ([]string, error) {
	lower, e := Lower(query)
	if e != nil {
		return nil, e
	}
	units, e := jsjson.UTF16(lower)
	if e != nil {
		return nil, e
	}
	if len(units) > 2000 {
		units = units[:2000]
	}
	lower = jsjson.FromUTF16(units)
	out := []string{}
	seen := map[string]bool{}
	add := func(t string) {
		_, all, count := hanClass(t)
		if len(out) >= 64 || generic[t] || seen[t] || (all && count < 2) {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	tokens, e := tokenRuns(lower)
	if e != nil {
		return nil, e
	}
	for _, token := range tokens {
		add(token)
		some, _, _ := hanClass(token)
		if some {
			words, e := WordLikeWords(token)
			if e != nil {
				return nil, e
			}
			for _, word := range words {
				some, _, _ := hanClass(word)
				if some {
					add(word)
				}
			}
		}
	}
	return out, nil
}
