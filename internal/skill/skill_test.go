package skill

import "testing"

func TestParseSkillFrontmatter(t *testing.T) {
	raw := `---
name: demo-skill
description: |
  first line
  second line
tags: [web, test]
---

# Body
`
	sk, err := parseSkill(raw, "test", "/tmp/demo-skill/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "demo-skill" || sk.Description != "first line second line" {
		t.Fatalf("unexpected metadata: %#v", sk)
	}
	if len(sk.Tags) != 2 || sk.Body != "# Body\n" {
		t.Fatalf("unexpected tags/body: %#v body=%q", sk.Tags, sk.Body)
	}
}
