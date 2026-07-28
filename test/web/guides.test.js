const assert = require("node:assert/strict");
const fs = require("node:fs");
const guides = require("../../web/guides.js");

const script = fs.readFileSync("web/app.js", "utf8");
const html = fs.readFileSync("web/index.html", "utf8");

// 설정 탭은 모두 모달 가이드를 가져야 합니다. 새 카테고리를 추가하면 이 테스트가
// 가이드 누락을 잡아 줍니다.
const categories = script
  .slice(script.indexOf("const categories = ["), script.indexOf("];", script.indexOf("const categories = [")))
  .match(/"([a-z]+)"/g)
  .map((value) => value.replaceAll('"', ""));
assert.ok(categories.length >= 19, `설정 카테고리 파싱 실패: ${categories.length}`);
for (const category of categories) {
  assert.ok(guides.has(category), `${category} 설정 가이드가 없습니다`);
}

// 화면에서 data-guide 로 여는 주제도 모두 존재해야 합니다.
for (const match of html.matchAll(/data-guide="([a-z_-]+)"/g)) {
  assert.ok(guides.has(match[1]), `${match[1]} 가이드가 없습니다`);
}

// ACL 가이드는 관리자 권한 문제 해결의 진입점이므로 필수 내용을 검사합니다.
const acl = guides.get("acl");
assert.equal(acl.diagnostics, true);
assert.ok(acl.sections.length >= 5);
assert.ok(acl.troubleshooting.length >= 3);
const aclText = JSON.stringify(acl);
for (const keyword of [
  "realmRoleMappings",
  "platform-admin",
  "bitbucket_user_slug",
  "gitlab_user_id",
  "recovery-token",
]) {
  assert.ok(aclText.includes(keyword), `ACL 가이드에 ${keyword} 설명이 없습니다`);
}

// 각 가이드는 제목·요약·본문 구조를 지켜야 렌더러가 동작합니다.
for (const topic of guides.topics()) {
  const guide = guides.get(topic);
  assert.ok(guide.title, `${topic} 제목 누락`);
  assert.ok(guide.summary, `${topic} 요약 누락`);
  assert.ok(Array.isArray(guide.sections) && guide.sections.length, `${topic} 섹션 누락`);
  for (const section of guide.sections) {
    assert.ok(section.title, `${topic} 섹션 제목 누락`);
    if (section.table) {
      assert.ok(Array.isArray(section.table.head) && Array.isArray(section.table.rows));
      for (const row of section.table.rows) {
        assert.equal(row.length, section.table.head.length, `${topic} 표 열 개수 불일치`);
      }
    }
  }
  for (const item of guide.troubleshooting || []) {
    assert.ok(item.symptom && item.fix, `${topic} 문제 해결 항목 불완전`);
  }
}

console.log("guide registry tests passed");
