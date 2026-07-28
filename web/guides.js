/**
 * git-ctx 설정 가이드 레지스트리.
 *
 * 화면 코드와 분리된 순수 데이터이므로, 새 설정 탭이나 운영 화면이 추가되면
 * 아래 객체에 항목 하나만 추가하면 모달 가이드가 그대로 동작합니다.
 *
 * 지원 필드
 *   title        모달 제목
 *   summary      한 문단 요약
 *   audience     이 설정을 바꿀 수 있는 플랫폼 역할
 *   sections[]   { title, body[], steps[], code, table:{head,rows}, notice }
 *   troubleshooting[] { symptom, cause, fix }
 *   diagnostics  true 이면 로그인 사용자의 실제 권한 진단 패널을 함께 표시
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  root.GitCtxGuides = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  const guides = {
    acl: {
      title: "ACL 설정 가이드 — 관리자가 설정을 저장하지 못할 때",
      audience: "platform-admin",
      diagnostics: true,
      summary:
        "git-ctx는 두 종류의 권한을 사용합니다. ① 화면과 설정 API를 여는 '플랫폼 역할', ② 저장소 문서를 검색할 수 있는 '소스 ACL Principal'입니다. 설정 저장이 403으로 막힌다면 ①이, 검색 결과가 0건이면 ②가 비어 있는 경우가 대부분입니다.",
      sections: [
        {
          title: "0. 관리자는 저장소 ACL 없이 검색합니다",
          body: [
            "platform-admin, source-admin, search-admin 역할은 카탈로그를 운영하는 역할이므로, Bitbucket·GitLab 계정이 매핑되지 않아도 등록된 모든 저장소와 원격 소스를 검색합니다.",
            "이 경우 검색 결과의 diagnostics에 'repository ACL checks are bypassed'가 표시되고, 내 계정 화면에도 '관리자 — 전체 저장소 검색'으로 나타납니다.",
            "그 외 역할(developer 등)은 기존대로 Fail-Closed입니다. claim이 없으면 결과가 0건입니다.",
          ],
          notice:
            "운영 역할에 실제 소스 접근 권한이 없어도 색인된 내용을 볼 수 있다는 뜻이므로, 세 역할은 꼭 필요한 담당자에게만 부여하세요.",
        },
        {
          title: "1. 두 가지 권한의 차이",
          table: {
            head: ["구분", "값의 출처", "없으면 생기는 증상"],
            rows: [
              ["플랫폼 역할", "Keycloak 역할 → 역할 매핑, 또는 사용자 관리 화면에서 직접 부여", "관리자 메뉴가 보이지 않거나 설정 저장 시 403 insufficient_role"],
              ["소스 ACL Principal", "Keycloak 토큰의 bitbucket_user_slug / gitlab_user_id claim", "검색·MCP 도구가 항상 0건 (Fail-Closed, 관리자 역할은 예외)"],
            ],
          },
        },
        {
          title: "2. 플랫폼 역할 종류",
          table: {
            head: ["역할", "권한 범위"],
            rows: [
              ["platform-admin", "모든 설정·사용자·백업·데이터베이스 (최고관리자)"],
              ["source-admin", "bitbucket, gitlab, confluence, jira, index 설정과 저장소 등록·재색인"],
              ["search-admin", "search, model, opensearch 설정과 검색 품질 벤치마크"],
              ["mcp-admin", "mcp 설정과 MCP 도구 운영"],
              ["security-admin", "security, vault 설정, 관리 Secret, API 키 강제 폐기, 감사"],
              ["auditor", "감사 로그 열람"],
              ["readonly-operator", "운영 상태·색인 현황 읽기 전용"],
              ["developer", "개인 MCP 키 발급과 검색 (기본값)"],
            ],
          },
        },
        {
          title: "3. Keycloak에서 역할 만들기",
          steps: [
            "Keycloak 관리 콘솔 → 해당 Realm → Realm roles → Create role 로 역할을 만듭니다. 이름을 git-ctx 플랫폼 역할과 동일하게(platform-admin 등) 지으면 별도 매핑 없이 자동 인식됩니다.",
            "사내 역할 명명 규칙 때문에 이름이 다르면(git-ctx-admin 등) 아래 4단계의 역할 매핑을 등록합니다.",
            "Users → 대상 사용자 → Role mapping → Assign role 로 역할을 부여합니다.",
            "Clients → git-ctx 클라이언트 → Client scopes → git-ctx-dedicated → Add mapper → By configuration → User Realm Role 을 추가하고 Token Claim Name 을 realm_access.roles, Add to access token 과 Add to ID token 을 모두 켭니다.",
          ],
          notice:
            "Keycloak 기본 설정에서도 realm_access.roles 는 access token 에 포함되지만, 클라이언트 scope 를 정리한 환경에서는 누락되는 경우가 많습니다. 로그인해도 역할이 developer 하나뿐이라면 이 매퍼를 먼저 확인하세요.",
        },
        {
          title: "4. git-ctx에서 역할 매핑 등록",
          steps: [
            "관리자 → 설정 → Keycloak 탭을 엽니다.",
            "'Keycloak 역할·Claim 매핑 (ACL)' 카드에서 Keycloak 역할 이름(예: git-ctx-admin), 매핑 종류(Realm/Client), 플랫폼 역할(platform-admin)을 고르고 [매핑 추가]를 누릅니다.",
            "[저장]을 누른 뒤 [Keycloak 로그인 시험]으로 다시 로그인하면 새 역할이 적용됩니다.",
            "역할은 로그인할 때마다 Keycloak 값으로 동기화됩니다. 사용자 관리 화면에서 역할을 직접 저장한 계정만 수동 관리로 전환되어 덮어쓰이지 않습니다.",
          ],
          code:
            '{\n  "realmRoleMappings": { "git-ctx-admin": "platform-admin", "git-ctx-source": "source-admin" },\n  "clientRoleMappings": { "admin": "platform-admin" }\n}',
        },
        {
          title: "5. 소스 ACL Principal claim 연결",
          steps: [
            "Keycloak → Clients → git-ctx → Client scopes → dedicated → Add mapper 로 사용자 속성을 토큰에 싣습니다.",
            "Bitbucket 사용: User Attribute 매퍼, User Attribute = bitbucketUserSlug, Token Claim Name = bitbucket_user_slug.",
            "GitLab 사용: User Attribute 매퍼, User Attribute = gitlabUserId, Token Claim Name = gitlab_user_id. 값은 GitLab 사용자의 숫자 ID입니다.",
            "그룹 기반 권한을 쓰면 Group Membership 매퍼를 추가하고 Token Claim Name 을 groups, Full group path 를 끕니다.",
            "claim 이름을 사내 표준으로 바꿔야 하면 Keycloak 탭의 매핑 카드에서 claim 이름을 직접 지정할 수 있습니다.",
          ],
          notice:
            "GitLab Internal 공개 범위 프로젝트는 gitlab:authenticated Principal 로 자동 허용됩니다. Private 프로젝트는 gitlab_user_id 가 실제 멤버 ID와 일치해야 검색됩니다.",
        },
        {
          title: "6. 모든 관리자가 잠겼을 때",
          steps: [
            "서버 콘솔에서 `git-ctx recovery-token --ttl 15m` 을 실행합니다.",
            "출력된 토큰을 /admin?recovery=1 화면의 [일회용 관리자 복구]에 입력하면 30분 제한 최고관리자 세션이 열립니다.",
            "Keycloak 설정과 역할 매핑을 고친 뒤 즉시 로그아웃합니다. 복구 세션으로는 영구 MCP 키를 만들 수 없습니다.",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "설정 저장 시 403 insufficient_role",
          cause: "토큰에 필요한 플랫폼 역할이 없습니다.",
          fix: "아래 진단 패널의 '현재 역할'을 확인하고, 3~4단계로 Keycloak 역할과 매핑을 등록한 뒤 다시 로그인하세요.",
        },
        {
          symptom: "관리자 메뉴 자체가 보이지 않음",
          cause: "역할이 developer 하나로만 동기화되었습니다.",
          fix: "Keycloak 클라이언트에 realm role 매퍼가 있는지, access token 포함 옵션이 켜져 있는지 확인하세요.",
        },
        {
          symptom: "검색·MCP 결과가 항상 0건",
          cause: "소스 ACL Principal 이 비어 있어 모든 저장소가 Fail-Closed 로 차단됩니다.",
          fix: "5단계 claim 매퍼를 추가하고 재로그인한 뒤, 진단 패널의 'ACL Principal' 값이 채워졌는지 확인하세요. 운영 담당자라면 source-admin 또는 search-admin 역할로도 즉시 검색할 수 있습니다.",
        },
        {
          symptom: "사용자 관리에서 역할을 바꿔도 로그인하면 되돌아감",
          cause: "해당 계정이 Keycloak 동기화 대상입니다.",
          fix: "사용자 관리 화면에서 저장하면 수동 관리로 전환됩니다. 그래도 되돌아가면 Keycloak 쪽 역할을 함께 정리하세요.",
        },
      ],
    },

    keycloak: {
      title: "Keycloak SSO 설정 가이드",
      audience: "platform-admin",
      diagnostics: true,
      summary:
        "Base URL, Realm, Client ID, Client Secret 네 값만 입력하면 Issuer 와 Redirect URL 은 자동으로 계산됩니다. 저장 전에 Discovery 연결을 검증하므로 잘못된 설정으로 로그인이 끊기지 않습니다.",
      sections: [
        {
          title: "1. Keycloak 클라이언트 준비",
          steps: [
            "Clients → Create client → Client type: OpenID Connect, Client ID: git-ctx.",
            "Client authentication: On (Confidential). Standard flow 만 켜고 Direct access grants 는 끕니다.",
            "Valid redirect URIs 에 <서비스 Public URL>/auth/callback 을 등록합니다.",
            "Valid post logout redirect URIs 에 <서비스 Public URL>/ 를 등록합니다.",
            "Web origins 에 <서비스 Public URL> 을 등록합니다.",
            "Credentials 탭에서 Client secret 을 복사합니다.",
          ],
        },
        {
          title: "2. git-ctx 입력값",
          table: {
            head: ["항목", "예시", "설명"],
            rows: [
              ["Keycloak Base URL", "https://sso.company.com", "/realms 앞까지의 주소. 마지막 슬래시는 없어도 됩니다."],
              ["Realm", "company", "Issuer 는 Base URL + /realms/<Realm> 으로 자동 구성됩니다."],
              ["Client ID", "git-ctx", "Keycloak 클라이언트 ID"],
              ["Client Secret", "••••", "Confidential 클라이언트의 시크릿. 저장 후에는 마스킹됩니다."],
            ],
          },
          notice:
            "Redirect URL 은 UI 설정의 '서비스 Public URL' 을 기준으로 자동 생성됩니다. 서비스 주소가 바뀌면 UI 탭의 Public URL 을 먼저 수정하세요.",
        },
        {
          title: "3. 검증 순서",
          steps: [
            "[설정 검증] — 입력값 형식과 Issuer 구성만 확인합니다.",
            "[연결 테스트·검증] — 실제 Discovery 문서와 JWKS 를 내려받아 확인합니다.",
            "[Claim·역할 미리보기] — 테스트 사용자의 토큰을 붙여 넣어 username, 역할, ACL claim 이 어떻게 해석되는지 확인합니다. 토큰은 저장·기록되지 않습니다.",
            "[Keycloak 로그인 시험] — 실제 Authorization Code + PKCE 로그인을 수행합니다.",
          ],
        },
        {
          title: "4. 역할과 ACL claim",
          body: [
            "역할 매핑과 소스 ACL claim 은 아래 'Keycloak 역할·Claim 매핑 (ACL)' 카드에서 설정합니다. 관리자 권한이 적용되지 않는 문제는 ACL 설정 가이드에 단계별로 정리되어 있습니다.",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "저장 시 OIDC discovery 오류",
          cause: "Base URL 오타, 사내 CA 미신뢰, 또는 Realm 이름 불일치입니다.",
          fix: "브라우저에서 <Base URL>/realms/<Realm>/.well-known/openid-configuration 이 열리는지 먼저 확인하세요.",
        },
        {
          symptom: "로그인 후 invalid redirect_uri",
          cause: "Keycloak 의 Valid redirect URIs 와 자동 생성된 Redirect URL 이 다릅니다.",
          fix: "UI 탭의 Public URL 과 Keycloak 의 redirect URI 를 동일하게 맞춥니다.",
        },
        {
          symptom: "로그인은 되는데 권한이 없음",
          cause: "역할 매핑 누락.",
          fix: "ACL 설정 가이드의 3~4단계를 수행하세요.",
        },
      ],
    },

    gitlab: {
      title: "GitLab 연동 가이드",
      audience: "platform-admin, source-admin",
      summary:
        "GitLab API v4 로 그룹·프로젝트를 탐색하고, 프로젝트 검색 API 와 Advanced Search 로 코드 본문을 검색합니다. 저장소 ACL 은 멤버 목록과 공개 범위로 판정합니다.",
      sections: [
        {
          title: "1. Access Token 준비",
          steps: [
            "그룹 또는 인스턴스 관리자 계정에서 Personal Access Token 을 만듭니다.",
            "필요한 scope: read_api (필수), read_repository (파일 수집에 필요).",
            "특정 그룹만 색인한다면 Group Access Token 으로도 충분합니다. 다만 그룹 밖 저장소는 검색되지 않습니다.",
            "만료일을 설정했다면 갱신 일정을 함께 등록하세요. 만료 시 색인과 검색이 동시에 실패합니다.",
          ],
        },
        {
          title: "2. 입력값",
          table: {
            head: ["항목", "예시", "설명"],
            rows: [
              ["GitLab Base URL", "https://gitlab.company.com", "/api/v4 는 자동으로 붙습니다."],
              ["Access Token", "glpat-…", "PRIVATE-TOKEN 헤더로 전송됩니다. secret://이름 참조도 가능합니다."],
              ["Code Search 검증 질의", "README", "연결 테스트가 실제로 던져 보는 검색어입니다."],
              ["Webhook Secret", "••••", "GitLab Push/Tag 이벤트 검증용 토큰입니다."],
              ["사내 CA PEM", "-----BEGIN CERTIFICATE-----", "사설 인증서를 쓰는 경우 붙여 넣습니다."],
            ],
          },
        },
        {
          title: "3. 코드 검색이 동작하는 방식",
          body: [
            "① Advanced Search(Elasticsearch)가 켜져 있으면 인스턴스 전체 blob 검색을 한 번에 수행합니다. GitLab 화면의 전역 코드 검색과 같은 경로입니다.",
            "② Advanced Search 가 없으면 프로젝트 단위 검색 API 로 전환해 접근 가능한 저장소를 순회합니다.",
            "③ 두 경우 모두 결과를 보여주기 전에 저장소 멤버·공개 범위로 ACL 을 재확인하고 Secret 마스킹을 적용합니다.",
            "검색 결과 화면의 diagnostics 항목에 어떤 경로가 사용됐는지, 몇 건이 ACL 로 걸러졌는지 표시됩니다.",
          ],
          notice:
            "Advanced Search 가 꺼진 인스턴스에서는 저장소 이름으로 후보를 좁힌 뒤 코드가 검색됩니다. 코드 본문만으로 전사 검색을 하려면 GitLab 관리자에게 Advanced Search 활성화를 요청하세요.",
        },
        {
          title: "4. 검증",
          steps: [
            "[연결 테스트·검증] — 그룹/프로젝트 조회, 멤버 ACL 조회, 브랜치 조회, 코드 검색 API 를 순서대로 확인합니다.",
            "'연동 즉시 검증' 카드에서 실제 검색어를 넣고 결과 건수와 저장소 목록을 바로 확인합니다.",
            "저장 후 소스·색인 화면에서 [소스 탐색] → [등록·색인] 으로 저장소를 등록합니다.",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "GitLab 화면에서는 검색되는데 git-ctx 결과가 0건",
          cause: "① 로그인 사용자의 gitlab_user_id claim 이 없어 ACL 이 모두 차단, ② Advanced Search 미사용, ③ 토큰이 해당 그룹에 접근 불가.",
          fix: "검색 결과의 diagnostics 문구를 먼저 확인하세요. 'no source ACL principal' 이면 ACL 가이드 5단계, 'instance-wide code search is unavailable' 이면 저장소 이름 또는 프로젝트를 함께 지정해 검색하세요.",
        },
        {
          symptom: "404 project not found",
          cause: "서브그룹 프로젝트의 네임스페이스 경로가 잘못 저장된 경우입니다.",
          fix: "[소스 탐색]으로 저장소를 다시 등록하면 전체 네임스페이스 경로로 갱신됩니다.",
        },
        {
          symptom: "401/403 gitlab API",
          cause: "토큰 만료 또는 scope 부족.",
          fix: "read_api scope 로 토큰을 재발급하고 설정에서 교체한 뒤 연결 테스트를 다시 실행하세요.",
        },
      ],
    },

    bitbucket: {
      title: "Bitbucket Server 연동 가이드",
      audience: "platform-admin, source-admin",
      summary:
        "Bitbucket Server 6.9.1 REST API 로 프로젝트·저장소를 수집하고 Code Search API 로 소스를 검색합니다. 권한은 저장소 사용자·그룹 권한을 그대로 사용합니다.",
      sections: [
        {
          title: "1. 자격 증명",
          steps: [
            "서비스 계정으로 Personal Access Token 을 만들고 Project read / Repository read 권한을 부여합니다.",
            "PAT 을 쓸 수 없는 환경이면 Username / Password 로 Basic 인증을 사용합니다.",
            "색인 대상 저장소 전체에 대한 읽기 권한이 있어야 ACL 동기화가 완전해집니다.",
          ],
        },
        {
          title: "2. 입력값",
          table: {
            head: ["항목", "기본값", "설명"],
            rows: [
              ["Bitbucket Base URL", "https://bitbucket.company.com", "컨텍스트 경로가 있으면 함께 입력합니다."],
              ["REST API Prefix", "/rest/api/1.0", "표준 배포에서는 그대로 둡니다."],
              ["Code Search API Path", "/rest/search/latest/search", "검색 플러그인 경로. 버전에 따라 다를 수 있습니다."],
              ["Code Search 검증 질의", "README", "연결 테스트에 사용할 검색어"],
            ],
          },
        },
        {
          title: "3. 검증",
          steps: [
            "[연결 테스트·검증] 으로 프로젝트 조회 → 저장소 조회 → 권한 조회 → 브랜치 조회 → 코드 검색을 순차 확인합니다.",
            "'연동 즉시 검증' 카드에서 실제 검색어로 결과를 확인합니다.",
            "검색이 skipped 로 표시되면 접근 가능한 프로젝트나 저장소가 없다는 뜻입니다.",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "search API 404",
          cause: "Code Search 플러그인 경로가 다릅니다.",
          fix: "Bitbucket 관리 콘솔에서 검색 엔드포인트를 확인해 Code Search API Path 를 수정하세요.",
        },
        {
          symptom: "권한 조회 실패",
          cause: "서비스 계정이 저장소 관리 권한 API 를 호출할 수 없습니다.",
          fix: "해당 프로젝트에 최소 Project read 권한을 부여하세요.",
        },
      ],
    },

    confluence: {
      title: "Confluence 연동 가이드",
      audience: "platform-admin, source-admin",
      summary:
        "Space 와 Page 를 문서로 수집합니다. Confluence 권한 모델은 저장소 ACL 과 다르므로, 허용 사용자·그룹을 명시적으로 지정하는 Fail-Closed 방식입니다.",
      sections: [
        {
          title: "1. 인증",
          steps: [
            "Bearer 방식: Confluence Personal Access Token 을 발급해 Token 에 입력합니다.",
            "Basic 방식: 서비스 계정 Username / Password 를 입력합니다.",
            "읽기 전용 계정을 권장합니다.",
          ],
        },
        {
          title: "2. 허용 Principal",
          body: [
            "허용 사용자·그룹에 입력한 값과 로그인 사용자의 ACL Principal 이 겹칠 때만 문서가 노출됩니다. 비워 두면 아무에게도 노출되지 않습니다(Fail-Closed).",
            "그룹은 group: 접두사를 사용합니다. 예: alice, group:platform-team",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "문서가 검색되지 않음",
          cause: "허용 Principal 이 비었거나 사용자 그룹 claim 과 다릅니다.",
          fix: "내 권한 진단에서 ACL Principal 목록을 확인해 동일한 값을 입력하세요.",
        },
      ],
    },

    jira: {
      title: "Jira 연동 가이드",
      audience: "platform-admin, source-admin",
      summary:
        "Project 와 Issue·Comment 를 지식으로 수집합니다. Confluence 와 동일하게 허용 Principal 기반 Fail-Closed ACL 을 사용합니다.",
      sections: [
        {
          title: "1. 인증과 범위",
          steps: [
            "Bearer(PAT) 또는 Basic 인증을 선택합니다.",
            "수집 대상 프로젝트에 Browse Projects 권한이 필요합니다.",
            "허용 사용자·그룹을 지정해야 검색 결과에 노출됩니다.",
          ],
        },
        {
          title: "2. 활용",
          body: [
            "장애 대응 이력이나 요구사항 논의를 MCP 검색에 포함하려는 경우에 사용합니다. 민감한 프로젝트는 허용 Principal 을 좁게 유지하세요.",
          ],
        },
      ],
    },

    mcp: {
      title: "MCP 설정 가이드",
      audience: "platform-admin, mcp-admin",
      summary:
        "MCP Streamable HTTP 엔드포인트(/mcp)의 호환 모드, 허용 Origin 과 요청 크기를 제어합니다.",
      sections: [
        {
          title: "1. 항목",
          table: {
            head: ["항목", "권장값", "설명"],
            rows: [
              ["Context7 Strict Compatibility", "끔", "켜면 resolve-library-id 와 query-docs 두 도구만 노출합니다. Context7 전용 클라이언트 호환이 필요할 때만 사용합니다."],
              ["허용 Origin", "https://ide.company.com", "브라우저 기반 MCP 클라이언트가 있을 때만 지정합니다. localhost 외에는 HTTPS 만 허용됩니다."],
              ["최대 요청 크기", "1048576", "1KiB~16MiB. 큰 컨텍스트를 보내는 클라이언트가 413 을 받으면 늘립니다."],
            ],
          },
        },
        {
          title: "2. 도구 활성화",
          body: [
            "개별 도구의 사용 여부, Timeout, 캐시는 관리자 → MCP 도구 운영 화면에서 조정합니다.",
            "사용자별 노출 범위는 API 키의 Scope 로 제한합니다.",
          ],
        },
      ],
    },

    search: {
      title: "검색 가중치 설정 가이드",
      audience: "platform-admin, search-admin",
      summary:
        "BM25 키워드 점수와 벡터 유사도를 결합한 하이브리드 검색의 가중치와 결과 수를 조정합니다.",
      sections: [
        {
          title: "1. 항목",
          table: {
            head: ["항목", "기본값", "조정 기준"],
            rows: [
              ["키워드 검색 가중치", "1", "정확한 식별자·에러 코드 검색이 중요하면 높입니다."],
              ["벡터 검색 가중치", "0.35", "자연어 질문 비중이 높으면 0.5 이상으로 올립니다."],
              ["초기 후보 수", "5000", "저장소가 많으면 늘리고, 응답이 느리면 줄입니다."],
              ["최종 문서 수", "8", "LLM 컨텍스트 예산에 맞춰 조정합니다."],
              ["재순위화 후보 수", "30", "Reranker 사용 시 상위 몇 건을 재정렬할지 정합니다."],
            ],
          },
        },
        {
          title: "2. 변경 후 확인",
          steps: [
            "검색 품질 화면에서 평가 사례를 등록합니다.",
            "[벤치마크 실행] 으로 Recall@K, MRR, nDCG 를 측정합니다.",
            "값이 기준선 아래로 떨어지면 이전 가중치로 되돌립니다.",
          ],
        },
      ],
    },

    model: {
      title: "Embedding·Reranker 모델 가이드",
      audience: "platform-admin, search-admin",
      summary:
        "기본은 외부 호출이 없는 256차원 로컬 임베딩입니다. 사내 OpenAI 호환 엔드포인트가 있으면 품질을 높일 수 있습니다.",
      sections: [
        {
          title: "1. Embedding",
          steps: [
            "Provider 를 openai-compatible 로 바꾸고 API URL, API Key, 모델명을 입력합니다.",
            "[연결 테스트·검증] 은 실제 임베딩 1회를 호출해 차원과 응답을 확인합니다.",
            "모델을 바꾸면 기존 벡터와 차원이 달라질 수 있으므로 저장소 재색인을 계획하세요.",
          ],
        },
        {
          title: "2. Reranker",
          body: [
            "Reranker 를 켜면 ACL 필터 이후 상위 후보를 사내 /v1/rerank 엔드포인트로 재정렬합니다.",
            "장애가 나면 자동으로 하이브리드 점수로 fallback 하므로 검색이 멈추지는 않습니다.",
          ],
        },
        {
          title: "3. 모델 미설정 시 동작",
          body: [
            "임베딩 모델이 설정되지 않으면 ACL 선검사 후 Bitbucket/GitLab 서버측 Query Search API 모드로 동작합니다. 색인 없이도 최신 소스를 검색할 수 있습니다.",
          ],
        },
      ],
    },

    opensearch: {
      title: "OpenSearch projection 가이드",
      audience: "platform-admin, search-admin",
      summary:
        "선택 기능입니다. 켜면 청크를 OpenSearch 에 증분 projection 해 BM25 후보 선별을 위임하고, 원문은 DB 에서 재검증합니다.",
      sections: [
        {
          title: "1. 준비",
          steps: [
            "인덱스 이름(기본 git-ctx-chunks)을 정하고 쓰기 권한이 있는 계정을 준비합니다.",
            "Basic 인증 또는 API Key 중 하나만 입력합니다.",
            "[연결 테스트·검증] 으로 클러스터 상태와 인덱스 접근을 확인합니다.",
          ],
        },
        {
          title: "2. 운영 주의",
          body: [
            "질의 단계에서도 ACL filter 가 적용되지만, 최종 결과는 항상 DB 원문으로 재검증합니다.",
            "OpenSearch 가 비어 있으면 자동으로 DB 후보 선별로 fallback 합니다.",
          ],
        },
      ],
    },

    index: {
      title: "색인 정책 가이드",
      audience: "platform-admin, source-admin",
      summary:
        "Webhook 이 누락된 변경을 보정하는 polling 주기와, 검색 최신성 SLO 를 설정합니다.",
      sections: [
        {
          title: "1. 항목",
          table: {
            head: ["항목", "기본값", "설명"],
            rows: [
              ["무결성 Polling 주기(분)", "30", "Webhook 유실 대비 전체 정합성 확인 주기"],
              ["검색 최신성 SLO(분)", "60", "이 시간을 넘긴 저장소는 소스·색인 화면에서 지연으로 표시됩니다."],
            ],
          },
        },
        {
          title: "2. 함께 확인할 것",
          steps: [
            "소스·색인 화면의 '최신성 SLO' 표에서 지연 저장소를 확인합니다.",
            "실패한 색인 작업은 같은 화면에서 [재시도] 할 수 있습니다.",
            "Webhook 이 등록되어 있으면 push/tag 이벤트로 즉시 증분 색인됩니다.",
          ],
        },
      ],
    },

    security: {
      title: "보안 설정 가이드",
      audience: "platform-admin, security-admin",
      summary:
        "신뢰할 수 있는 프록시 대역을 지정해 클라이언트 IP 판정과 API 키 CIDR 제한이 정확히 동작하도록 합니다.",
      sections: [
        {
          title: "1. 신뢰 Proxy CIDR",
          body: [
            "이 목록에 포함된 원격 주소에서 온 요청만 X-Forwarded-For 를 신뢰합니다.",
            "지정하지 않으면 모든 요청의 IP 는 TCP 연결 주소로 판정되어, 프록시 뒤에서는 API 키 CIDR 제한이 의도대로 동작하지 않습니다.",
            "예: 10.0.0.0/8, 192.168.0.0/16",
          ],
        },
        {
          title: "2. 함께 볼 화면",
          body: [
            "보안 화면에서 관리 Secret 등록·회전, 전체 API 키 강제 폐기, 색인 보안 이벤트를 확인할 수 있습니다.",
          ],
        },
      ],
    },

    vault: {
      title: "Vault KV v2 가이드",
      audience: "platform-admin, security-admin",
      summary:
        "설정값에 secret://이름 을 쓰면 실행 시점에 Vault 또는 암호화 DB 에서 해석합니다. 원문은 설정 이력에 남지 않습니다.",
      sections: [
        {
          title: "1. 설정",
          steps: [
            "Vault URL, Token, KV v2 Mount(기본 secret), 경로 Prefix(기본 git-ctx)를 입력합니다.",
            "Enterprise 라면 Namespace 를 함께 입력합니다.",
            "[연결 테스트·검증] 으로 mount 접근을 확인합니다.",
          ],
        },
        {
          title: "2. 사용법",
          steps: [
            "보안 화면에서 Backend 를 Vault 로 지정해 비밀정보를 등록합니다.",
            "GitLab Access Token 같은 필드에 secret://gitlab-token 형태로 입력합니다.",
            "회전은 같은 이름으로 다시 등록하면 되고, 참조 설정은 수정할 필요가 없습니다.",
          ],
        },
      ],
    },

    notifications: {
      title: "알림 설정 가이드",
      audience: "platform-admin",
      summary:
        "인앱 알림과 Webhook·사내 메신저·SMTP 외부 전달을 구성합니다. 전달은 Outbox 로 재시도되며 이력이 남습니다.",
      sections: [
        {
          title: "1. 인앱 알림",
          table: {
            head: ["항목", "설명"],
            rows: [
              ["인앱 보안·만료 알림", "API 키 만료, 보안 이벤트를 사용자 화면에 표시"],
              ["API 키 만료 사전 알림일", "0 이면 해제, 7 이면 만료 7일 전 알림"],
              ["API 키 호출량 초과 알림", "분/시/일 제한 초과 시 알림"],
            ],
          },
        },
        {
          title: "2. 외부 전달",
          steps: [
            "외부 알림 전송을 켜고 Webhook URL 또는 사내 메신저 Webhook URL 을 입력합니다.",
            "SMTP 를 쓰면 Host, Port, 발신 주소, TLS 방식을 입력하고 '연결 시험 수신 이메일' 에 테스트 주소를 넣습니다.",
            "[연결 테스트·검증] 은 실제 전송을 시도합니다. 수신함을 확인하세요.",
            "실패한 전달은 보안 화면의 '외부 알림 전달' 표에서 [재시도] 할 수 있습니다.",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "SMTP 연결 시험 실패",
          cause: "TLS 모드 불일치 또는 방화벽 차단.",
          fix: "465 포트는 tls, 587 포트는 starttls 를 선택하고 아웃바운드 방화벽을 확인하세요.",
        },
      ],
    },

    logging: {
      title: "로그 레벨 가이드",
      audience: "platform-admin",
      summary: "재기동 없이 즉시 적용되는 구조화 로그 레벨입니다.",
      sections: [
        {
          title: "레벨 선택",
          table: {
            head: ["레벨", "사용 시점"],
            rows: [
              ["debug", "장애 재현·연동 디버깅 (요청 상세 증가)"],
              ["info", "평시 운영 기본값"],
              ["warn", "로그량을 줄여야 하는 환경"],
              ["error", "오류만 수집"],
            ],
          },
          notice: "debug 는 로그량이 급증합니다. 조사 후 반드시 info 로 되돌리세요.",
        },
      ],
    },

    observability: {
      title: "OpenTelemetry 설정 가이드",
      audience: "platform-admin",
      summary:
        "OTLP HTTP 로 trace 를 내보내고 W3C trace context 를 전파합니다.",
      sections: [
        {
          title: "1. 설정",
          steps: [
            "OpenTelemetry 사용을 켜고 OTLP HTTP Endpoint 를 입력합니다. 예: https://otel.company.com/v1/traces",
            "Sample Ratio 는 0~1 입니다. 운영 초기에는 0.1 정도를 권장합니다.",
            "추가 헤더가 필요하면 JSON 으로 입력합니다. 예: {\"x-api-key\":\"…\"}",
            "로컬 개발에서 http 로 보내려면 'Localhost HTTP 허용' 을 켭니다.",
          ],
        },
        {
          title: "2. 확인",
          body: [
            "[연결 테스트·검증] 후 운영 상태 화면의 Trace 항목이 활성으로 바뀝니다.",
          ],
        },
      ],
    },

    backup: {
      title: "백업 설정 가이드",
      audience: "platform-admin",
      summary:
        "Master Key 로 암호화된 예약 백업을 만들고 보존 개수를 관리합니다. 복원하면 모든 세션이 폐기됩니다.",
      sections: [
        {
          title: "1. 설정",
          table: {
            head: ["항목", "기본값", "설명"],
            rows: [
              ["예약 백업 사용", "끔", "켜면 주기적으로 자동 백업합니다."],
              ["백업 디렉터리", "/var/lib/git-ctx/backups", "컨테이너에서는 영구 볼륨이어야 합니다."],
              ["백업 주기(시간)", "24", "운영 변경이 잦으면 6~12 시간을 권장합니다."],
              ["보존 개수", "7", "초과분은 오래된 순으로 삭제됩니다."],
              ["백업 최대 크기", "536870912", "초과 시 백업이 실패로 기록됩니다."],
            ],
          },
        },
        {
          title: "2. 복원 절차",
          steps: [
            "백업·복구 화면에서 대상 백업의 SHA-256 을 확인합니다.",
            "[복원] 을 누르고 안내된 확인 문구를 정확히 입력합니다.",
            "복원 후 모든 사용자가 다시 로그인해야 합니다.",
          ],
        },
      ],
    },

    retention: {
      title: "데이터 보존 가이드",
      audience: "platform-admin",
      summary:
        "감사·호출·알림·색인 운영 데이터의 수명주기를 정합니다. 0 은 영구 보관입니다.",
      sections: [
        {
          title: "권장값",
          table: {
            head: ["대상", "권장", "비고"],
            rows: [
              ["감사 로그", "365", "내부 통제 요건에 맞춰 조정"],
              ["MCP 호출 기록", "90", "사용량 분석 기간"],
              ["사용자 알림", "90", ""],
              ["Webhook 이벤트", "30", "멱등 처리 후 재사용되지 않음"],
              ["색인 작업", "30", "완료·실패 작업만 정리"],
              ["색인 보안 이벤트", "180", "Secret 탐지 이력"],
              ["과거 설정 버전", "365", "변경 추적 근거"],
            ],
          },
        },
      ],
    },

    operations: {
      title: "운영 설정 가이드",
      audience: "platform-admin",
      summary:
        "수신 주소와 Timeout, 점검 모드를 설정합니다. 점검 모드만 즉시 반영되고 나머지는 재기동 후 적용됩니다.",
      sections: [
        {
          title: "1. 즉시 반영",
          body: [
            "점검 모드를 켜면 일반 요청은 안내 메시지와 함께 차단되고, 관리자 화면과 상태 엔드포인트는 계속 동작합니다.",
            "점검 안내에 사유와 예상 종료 시각을 함께 적어 두세요.",
          ],
        },
        {
          title: "2. 재기동 필요",
          table: {
            head: ["항목", "기본값", "설명"],
            rows: [
              ["서비스 수신 주소", ":4747", "컨테이너 포트와 함께 변경해야 합니다."],
              ["요청 헤더 Timeout", "10", "Slowloris 방어"],
              ["요청 읽기 / 응답 쓰기", "30 / 60", "대용량 export 가 잘리면 늘립니다."],
              ["유휴 연결 Timeout", "90", "프록시 keep-alive 보다 길게 유지"],
              ["종료 대기", "15", "무중단 배포 시 진행 중 요청 대기 시간"],
            ],
          },
        },
      ],
    },

    ui: {
      title: "UI·브랜딩 가이드",
      audience: "platform-admin",
      summary:
        "서비스 Public URL, 이름, 로고와 공지를 설정합니다. Public URL 은 Keycloak Redirect URL 계산에도 사용됩니다.",
      sections: [
        {
          title: "주의",
          body: [
            "서비스 Public URL 은 localhost 를 제외하면 HTTPS 여야 합니다.",
            "이 값을 바꾸면 Keycloak Redirect URL 이 자동으로 다시 계산되므로, Keycloak 클라이언트의 Valid redirect URIs 도 함께 수정하세요.",
            "로고·파비콘 URL 은 같은 오리진의 정적 파일 경로를 권장합니다.",
          ],
        },
      ],
    },

    users: {
      title: "사용자·역할 관리 가이드",
      audience: "platform-admin",
      diagnostics: true,
      summary:
        "Keycloak 사용자를 등록하고 플랫폼 역할을 부여합니다. 이 화면에서 저장한 계정은 수동 관리로 전환되어 로그인 시 Keycloak 역할로 덮어써지지 않습니다.",
      sections: [
        {
          title: "1. 사용자 추가",
          steps: [
            "Keycloak 관리 콘솔에서 대상 사용자의 Subject(UUID)를 복사합니다.",
            "[사용자 추가] 후 Subject, 사용자명, 이메일을 입력하고 역할을 선택합니다.",
            "저장하면 해당 계정은 수동 역할 관리 대상이 됩니다.",
          ],
        },
        {
          title: "2. 비활성화와 삭제",
          body: [
            "상태를 비활성으로 바꾸면 로그인과 API 키 사용이 즉시 차단됩니다.",
            "삭제하면 세션과 API 키가 함께 폐기됩니다.",
          ],
        },
      ],
    },

    "api-keys": {
      title: "MCP API 키 발급 가이드",
      summary:
        "AI 개발 도구가 사용할 개인 키를 발급합니다. 원문은 생성 직후 한 번만 표시되며 HMAC 으로만 저장됩니다.",
      sections: [
        {
          title: "1. 발급",
          steps: [
            "[새 키 만들기] 에서 이름과 만료일을 정합니다. 만료일 없는 키는 권장하지 않습니다.",
            "허용 도구(Scope)는 실제로 쓰는 것만 선택합니다.",
            "필요하면 허용 CIDR, 허용 저장소 Library ID, 분/시/일 호출 제한을 지정합니다.",
            "생성 직후 표시되는 키를 환경변수(GIT_CTX_API_KEY)에 저장합니다.",
          ],
        },
        {
          title: "2. 운영",
          table: {
            head: ["동작", "설명"],
            rows: [
              ["Scope 편집", "발급 후에도 허용 도구를 조정할 수 있습니다."],
              ["중지 / 재활성화", "일시적으로 사용을 막습니다."],
              ["회전", "중복 유효 시간을 두고 새 키를 발급합니다. 무중단 교체에 사용합니다."],
              ["폐기", "즉시 사용 불가. 되돌릴 수 없습니다."],
            ],
          },
        },
      ],
    },

    "mcp-client": {
      title: "MCP 클라이언트 연결 가이드",
      summary:
        "Claude Code, Codex 등 MCP 클라이언트에 git-ctx 를 등록합니다. Streamable HTTP 엔드포인트는 /mcp 입니다.",
      sections: [
        {
          title: "1. 설정 예시",
          code:
            '{\n  "mcpServers": {\n    "git-ctx": {\n      "url": "https://git-ctx.company.com/mcp",\n      "headers": { "CONTEXT7_API_KEY": "${GIT_CTX_API_KEY}" }\n    }\n  }\n}',
        },
        {
          title: "2. 확인",
          steps: [
            "클라이언트에서 tools/list 가 보이면 연결 성공입니다.",
            "401 이면 키가 만료·폐기되었거나 헤더 이름이 다릅니다.",
            "403 이면 키 Scope 에 해당 도구가 없습니다. API 키 화면에서 Scope 를 편집하세요.",
          ],
        },
      ],
    },

    "code-search": {
      title: "코드 지식 검색 사용 가이드",
      diagnostics: true,
      summary:
        "색인된 문서와 원격 소스를 함께 검색합니다. 모든 결과는 로그인 사용자의 소스 ACL 로 필터링됩니다.",
      sections: [
        {
          title: "1. 검색 팁",
          steps: [
            "저장소를 찾을 때는 이름만 입력합니다. 예: dify",
            "코드 본문을 찾을 때는 식별자나 문자열을 그대로 입력합니다. 예: NewOIDCVerifier",
            "결과가 많으면 소스, 프로젝트, 저장소, Ref 를 함께 지정해 좁힙니다.",
            "'~ 소스 검색해' 같은 한국어 명령 표현은 자동으로 제거되고 핵심어만 원격 API 로 전달됩니다.",
          ],
        },
        {
          title: "2. MCP 도구 선택",
          table: {
            head: ["도구", "언제 쓰나", "반환"],
            rows: [
              ["search-code", "코드·심볼·설정 위치를 찾을 때 (기본)", "저장소 + 파일 내용 + 실행 경로 Notes"],
              ["search-repositories", "저장소 이름만 찾을 때", "저장소 목록 (파일 내용 없음)"],
              ["query-docs", "특정 Library ID의 문서를 물을 때", "색인 결과, 없으면 소스 API failover"],
              ["find-symbol", "정확한 식별자를 알 때", "심볼 정의 위치"],
            ],
          },
          notice:
            "코드 질문에 search-repositories만 호출하면 파일 내용이 나오지 않습니다. 결과 안내 문구가 search-code 재호출을 유도하도록 되어 있습니다.",
        },
        {
          title: "3. 결과가 비어 있을 때",
          body: [
            "응답의 Diagnostics 항목에 실행 경로와 차단 사유가 표시됩니다.",
            "'no source ACL principal' 이면 권한 진단에서 ACL Principal 을 확인하세요.",
            "'instance-wide code search is unavailable' 이면 GitLab Advanced Search 가 꺼져 있는 것이므로 저장소나 프로젝트를 함께 지정하세요.",
            "'did not finish within the tool timeout' 이면 저장소·프로젝트로 범위를 좁히거나 MCP 도구 화면에서 Timeout 을 늘리세요.",
            "색인이 아직 없는 저장소도 query-docs 가 소스 Code Search API 로 failover 하므로 즉시 사용할 수 있습니다.",
          ],
        },
      ],
    },

    indexing: {
      title: "소스·색인 운영 가이드",
      audience: "platform-admin, source-admin",
      summary:
        "저장소를 탐색해 등록하고, 색인 작업과 최신성을 관리합니다.",
      sections: [
        {
          title: "1. 등록",
          steps: [
            "소스를 고르고 프로젝트 Key(또는 GitLab Group ID)를 비운 채 [소스 탐색] 하면 프로젝트 목록이 나옵니다.",
            "프로젝트를 지정해 다시 탐색하면 저장소 목록이 나오고, [등록·색인] 으로 초기 색인 작업이 생성됩니다.",
            "등록 저장소 표에서 언제든 [재색인] 할 수 있습니다.",
          ],
        },
        {
          title: "2. 색인 대상 파일",
          body: [
            "기본 정책은 문서·설정 파일과 주요 언어 확장자를 포함하고, Dockerfile·Makefile 처럼 확장자가 없는 빌드 파일도 이름으로 인식합니다.",
            "저장소마다 [색인 정책] 버튼으로 확장자, 제외 경로, 파일 최대 크기를 조정할 수 있고 저장하면 즉시 재색인 작업이 생성됩니다.",
          ],
          notice:
            "작업이 '완료'인데 파일 수가 0이면 정책에 맞는 파일이 하나도 없다는 뜻입니다. 이 경우 작업 오류란에 'none matched the index policy' 경고가 표시됩니다.",
        },
        {
          title: "3. 문제 대응",
          table: {
            head: ["상태", "의미", "조치"],
            rows: [
              ["pending", "작업 대기", "Worker 동작과 DB 연결을 확인"],
              ["running", "수집 중", "처리 파일 수가 주기적으로 갱신되며 화면도 자동 새로고침됩니다"],
              ["failed", "수집 실패", "오류 메시지 확인 후 [재시도]"],
              ["completed + 경고", "일부 파일 건너뜀", "건너뛴 파일명과 사유가 오류란에 표시됩니다"],
              ["stale", "SLO 초과", "Webhook 등록 여부와 polling 주기 확인"],
            ],
          },
        },
        {
          title: "4. 임베딩 모델을 함께 쓸 때",
          body: [
            "청크는 최대 32개씩 묶어 임베딩 API로 전송하고, 429·5xx 응답은 지수 백오프로 재시도합니다.",
            "모델명 오류 같은 4xx 응답은 재시도 없이 즉시 작업 실패로 기록되므로 오류 메시지에서 원인을 바로 확인할 수 있습니다.",
            "모델을 바꾸면 embedding revision이 달라져 전체 재색인이 수행됩니다. 대상 저장소가 많으면 작업 시간을 고려하세요.",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "등록은 되는데 색인 파일 수가 0",
          cause: "저장소 언어가 색인 정책에 없습니다.",
          fix: "등록 저장소 목록에서 [색인 정책]을 열어 해당 확장자를 추가하고 저장하세요.",
        },
        {
          symptom: "저장소 등록이 400 webhook_secret_required로 막힘",
          cause: "Webhook 자동 등록이 켜져 있는데 Webhook Secret이 비어 있습니다.",
          fix: "소스 설정에서 Webhook Secret을 입력하거나 '저장소 등록 시 Webhook 자동 등록'을 끄세요.",
        },
        {
          symptom: "색인 작업이 계속 실패하고 재시도만 반복",
          cause: "임베딩 엔드포인트 오류이거나 모든 파일을 내려받지 못한 경우입니다.",
          fix: "작업 오류 메시지를 확인하고, 모델 탭에서 [연결 테스트·검증]으로 임베딩 호출을 먼저 확인하세요.",
        },
      ],
    },

    quality: {
      title: "검색 품질 관리 가이드",
      audience: "platform-admin, search-admin",
      summary:
        "Context Pack 으로 검색 범위를 묶고, 정답 데이터셋으로 검색 품질 회귀를 감시합니다.",
      sections: [
        {
          title: "1. Context Pack",
          steps: [
            "Slug 와 이름을 정하고 Library 항목을 한 줄에 하나씩 입력합니다.",
            "형식: /library-id|ref|질의 힌트  (ref 와 힌트는 생략 가능)",
            "MCP 의 get-context-pack 도구가 이 범위를 사용합니다.",
          ],
        },
        {
          title: "2. 벤치마크",
          steps: [
            "평가 사례에 질의, ACL Principal, 정답 파일 목록을 등록합니다.",
            "Top K 와 최소 Recall/MRR/nDCG 기준을 정하고 [벤치마크 실행] 합니다.",
            "실패하면 검색 가중치나 모델 설정을 되돌리고 다시 측정합니다.",
          ],
        },
        {
          title: "3. 사용자 검색 진단",
          body: [
            "특정 사용자가 '검색이 안 된다'고 할 때, 그 사용자의 ACL Principal 로 같은 질의를 재현해 원인을 구분합니다.",
            "역할·ACL Principal 매핑 상태, 저장소별 결과 수, 실행된 검색 경로가 함께 표시됩니다.",
            "코드 조각과 파일 경로는 반환하지 않으므로, 소스 접근 권한이 없는 관리자에게 내용이 노출되지 않습니다. 실행 사실은 감사 로그에 남습니다.",
          ],
        },
      ],
      troubleshooting: [
        {
          symptom: "진단 결과 ACL Principal 이 '매핑 없음'",
          cause: "Keycloak claim 매퍼가 없어 소스 신원이 비어 있습니다.",
          fix: "ACL 설정 가이드 5단계의 claim 매퍼를 추가하고 해당 사용자가 다시 로그인하게 합니다.",
        },
        {
          symptom: "ACL 은 정상인데 결과가 0건",
          cause: "저장소가 아직 등록·색인되지 않았거나 원격 검색 경로가 제한되어 있습니다.",
          fix: "진단 결과의 diagnostics 문구를 확인하고, 초기 설정 진행 상황 카드에서 남은 단계를 처리하세요.",
        },
      ],
    },

    setup: {
      title: "초기 설정 진행 가이드",
      audience: "platform-admin",
      diagnostics: true,
      summary:
        "새 인스턴스는 Keycloak → ACL claim → 소스 연결 → 저장소 등록 → 색인 순으로 준비해야 검색 결과가 나옵니다. 관리자 화면 상단의 진행 상황 카드가 각 단계를 실시간으로 판정합니다.",
      sections: [
        {
          title: "단계별 의미",
          table: {
            head: ["단계", "완료 조건", "미완료 시 증상"],
            rows: [
              ["Keycloak SSO 연결", "설정 저장 + Discovery 적용 성공", "SSO 로그인 불가, 최초 관리자 토큰으로만 접근"],
              ["소스 ACL Claim 매핑", "사용자에게 Bitbucket·GitLab 신원이 매핑됨", "로그인은 되지만 모든 검색이 0건"],
              ["소스 시스템 연결", "bitbucket/gitlab/confluence/jira 중 하나 저장", "검색 대상 자체가 없음"],
              ["저장소 등록", "활성 저장소 1개 이상", "Library ID 기반 도구가 동작하지 않음"],
              ["초기 색인", "청크 1개 이상, 실패 작업 0개", "원격 검색만 가능하고 문서 검색은 비어 있음"],
              ["백업 예약", "backup 설정 저장", "장애 시 복구 지점 없음"],
            ],
          },
          notice: "각 카드의 [이동] 버튼은 해당 설정 탭이나 운영 화면으로 바로 이동합니다.",
        },
      ],
    },

    security_ops: {
      title: "보안 운영 가이드",
      audience: "platform-admin, security-admin",
      summary: "관리 Secret, API 키, 색인 보안 이벤트를 다룹니다.",
      sections: [
        {
          title: "관리 Secret",
          steps: [
            "이름과 값을 등록하면 설정에서 secret://이름 으로 참조할 수 있습니다.",
            "같은 이름으로 다시 등록하면 회전되고 버전이 올라갑니다.",
            "중지하면 참조하는 설정이 즉시 실패하므로, 교체 후 중지하세요.",
          ],
        },
      ],
    },

    audit: {
      title: "감사 로그 가이드",
      audience: "platform-admin, security-admin, auditor",
      summary:
        "설정 변경, 로그인, 키 운영, 백업·복원 등 관리 행위가 기록됩니다.",
      sections: [
        {
          title: "읽는 방법",
          table: {
            head: ["열", "의미"],
            rows: [
              ["수행자", "행위를 수행한 사용자 ID"],
              ["행위", "settings.update, login, apikey.revoke 등"],
              ["대상", "리소스 종류와 식별자"],
              ["결과", "success 또는 failure"],
            ],
          },
          notice: "설정 값 원문과 Secret 은 기록되지 않습니다. 버전 번호만 남습니다.",
        },
      ],
    },

    database: {
      title: "메타 데이터베이스 가이드",
      audience: "platform-admin",
      summary:
        "PostgreSQL 연결 상태를 진단하고, SQLite 복구 모드에서 PostgreSQL 로 논리 이전합니다.",
      sections: [
        {
          title: "1. 상태 읽기",
          body: [
            "기동 모드가 'SQLite 복구 모드' 면 PostgreSQL 연결에 실패해 backups/recovery.db 로 기동된 상태입니다.",
            "Migration 개수와 최신 버전으로 스키마 적용 상태를 확인할 수 있습니다.",
          ],
        },
        {
          title: "2. PostgreSQL 전환",
          steps: [
            "새 DSN 을 입력하고 [연결 시험] 으로 읽기 전용 확인을 먼저 합니다.",
            "확인란에 MIGRATE TO POSTGRES 를 정확히 입력한 뒤 [데이터 이전] 을 실행합니다.",
            "완료 후 서비스를 재시작하면 암호화 저장된 DSN 으로 기동합니다.",
          ],
          notice:
            "설정 암호화 키는 최초 Bootstrap DSN 에서 파생됩니다. 환경변수의 Bootstrap DSN 문자열을 임의로 바꾸면 기존 설정을 복호화할 수 없습니다.",
        },
      ],
    },
  };

  function get(topic) {
    return (topic && guides[topic]) || null;
  }

  function has(topic) {
    return Boolean(get(topic));
  }

  function topics() {
    return Object.keys(guides);
  }

  return { get, has, topics, guides };
});
