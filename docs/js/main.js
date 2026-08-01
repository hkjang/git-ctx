/**
 * git-ctx Landing Page Interactive Scripts
 * Internationalization (KO / EN), Tool Explorer Tabs, Copy Snippets, Accordions
 */

document.addEventListener('DOMContentLoaded', () => {
  initLanguageSwitcher();
  initToolExplorer();
  initFaqAccordion();
  initCopyButtons();
  initMobileNav();
});

/* Internationalization Data & Logic */
const i18nData = {
  ko: {
    hero_pill: "온프레미스 AI 개발 지식 플랫폼 v1.0",
    hero_title: "사내 <span class=\"highlight\">Bitbucket & GitLab</span> 소스코드를 AI 에이전트의 실시간 지식으로",
    hero_desc: "Model Context Protocol (MCP) 기반 두 단계 하이브리드 검색. 강력한 ACL 권한 유지, 보안 파싱, AST 코드 구조 분석으로 보안 훼손 없는 Enterprise AI 개발 환경을 구축하세요.",
    btn_quickstart: "빠른 시작 가이드",
    btn_github: "GitHub 저장소",
    stat_mcp: "2단계 MCP",
    stat_mcp_desc: "Context7 연동 표준 계약",
    stat_security: "Zero Data Leak",
    stat_security_desc: "Keycloak OIDC & ACL 선필터",
    stat_ast: "AST 심볼 그래프",
    stat_ast_desc: "Go, Java, TS, Py, SQL 분석",
    stat_deploy: "온프레미스 / 폐쇄망",
    stat_deploy_desc: "Docker / K8s / Air-gapped",

    features_subtitle: "KEY FEATURES",
    features_title: "엔터프라이즈 AI 코딩을 위한 핵심 기능",
    features_desc: "git-ctx는 외부 유출 없는 온프레미스 환경에서 AI 모델에 가장 정확하고 안전한 지식을 제공합니다.",
    
    feat_1_title: "완벽한 보안 및 ACL 권한 검증",
    feat_1_desc: "Keycloak OIDC 및 Bitbucket/GitLab ACL을 검색 후보 생성 선단계에서 100% 검증합니다. 권한 없는 소스코드와 문서는 노출되지 않습니다.",

    feat_2_title: "Context7 호환 2단계 MCP 흐름",
    feat_2_desc: "resolve-library-id → query-docs/search-code 구조로 에이전트의 토큰 소모를 극대화로 줄이고 정교한 컨텍스트 조각만 주입합니다.",

    feat_3_title: "AST 코드 구조 & 심볼 그래프",
    feat_3_desc: "Go, Java, TypeScript, Python, SQL 구조 파싱 기반으로 find-symbol, trace-dependencies, find-dependents 심볼 영향도 분석을 지원합니다.",

    feat_4_title: "하이브리드 BM25 + 임베딩 검색",
    feat_4_desc: "PostgreSQL tsvector 키워드 검색과 사내 OpenAI 호환 벡터 검색을 결합합니다. 모델 장애 시 safe-lexical 자동 폴백이 작동합니다.",

    feat_5_title: "X-Ray 감사 및 검색 설명서",
    feat_5_desc: "MCP 호출 감사 로그, 단계별 수용/탈락 근거(Diagnostics), latency 및 explain-search-result로 투명한 운영 상태를 파악하세요.",

    feat_6_title: "폐쇄망(Air-gapped) 원클릭 배포",
    feat_6_desc: "인터넷 연결이 차단된 환경에서도 오프라인 이미지 패키징, K8s Kustomize, Docker Compose, SQLite/PostgreSQL 이중 복구 모드를 제공합니다.",

    tools_subtitle: "MCP TOOL ECOSYSTEM",
    tools_title: "AI 에이전트와 소통하는 MCP 도구 모음",
    tools_desc: "Cursor, Claude Desktop, Antigravity 등 최신 AI 개발 도구에서 바로 사용할 수 있는 풍부한 도구를 제공합니다.",

    arch_subtitle: "ARCHITECTURE",
    arch_title: "엔터프라이즈 하이브리드 지식 파이프라인",
    arch_desc: "요청 수신부터 ACL 인가, 2단계 지식 정제까지 보안 중심의 모듈형 구조",

    arch_step1_title: "AI Agent & IDE",
    arch_step1_desc: "Cursor, Antigravity, Claude Desktop 등의 MCP 클라이언트 요청",
    arch_step2_title: "MCP /mcp Endpoint",
    arch_step2_desc: "Keycloak Bearer / API Key 인증 및 Streamable HTTP 통신",
    arch_step3_title: "ACL & Source Adapter",
    arch_step3_desc: "Bitbucket 6.9.1 / GitLab ACL 선필터링 및 증분 수집",
    arch_step4_title: "Hybrid Retrieval",
    arch_step4_desc: "BM25 + Vector + Rerank 및 AST 심볼 그래프 컨텍스트 추출",

    quickstart_subtitle: "QUICK START",
    quickstart_title: "몇 분 만에 구축하는 git-ctx",
    quickstart_desc: "단 두 개의 환경 변수 설정으로 바로 온프레미스 서버를 기동하고 MCP 클라이언트를 연결할 수 있습니다.",

    step1_title: "1. git-ctx 서비스 기동 (Docker 또는 Go 실행)",
    step1_desc: "장기 복구 키(GIT_CTX_RECOVERY_KEY)와 DB 접속 DSN을 지정하여 서버를 기동합니다.",
    step2_title: "2. 최초 관리자 복구 토큰 확인 & SSO 설정",
    step2_desc: "최초 기동 시 생성되는 backups/bootstrap-admin.token으로 관리 콘솔 로그인 후 Keycloak OIDC를 연결합니다.",
    step3_title: "3. MCP 클라이언트 설정 (예: claude_desktop_config.json)",
    step3_desc: "발급받은 MCP API Key를 사용하여 에이전트에 git-ctx MCP 서버를 등록합니다.",

    faq_subtitle: "FAQ & AEO",
    faq_title: "자주 묻는 질문 (FAQ)",
    faq_desc: "git-ctx 도입 및 운영에 관한 주요 궁금증을 확인하세요.",

    faq_1_q: "Q1. git-ctx란 무엇이며 왜 필요한가요?",
    faq_1_a: "git-ctx는 사내 Bitbucket Server 6.9.1과 GitLab의 소스코드 및 개발 문서를 색인하여 Context7 호환 MCP(Model Context Protocol)로 AI 에이전트에 제공하는 온프레미스 지식 플랫폼입니다. 사내 코드가 외부로 유출되지 않으면서도 개발자 AI 에이전트가 사내 전체 코드베이스와 문서 문맥을 실시간 활용할 수 있게 합니다.",

    faq_2_q: "Q2. Context7 호환 2단계 MCP 흐름의 장점은 무엇인가요?",
    faq_2_a: "전체 저장소를 통째로 토큰에 넣는 대신 1단계 resolve-library-id로 대상 저장소/버전을 탐색하고 2단계 query-docs / search-code로 필요한 코드 청크와 문맥만 정확히 검색합니다. 이를 통해 LLM 프롬프트 비용을 최대 90% 절감하고 첫 호출 정확도를 극대화합니다.",

    faq_3_q: "Q3. 사내 코드 보안과 접근 권한(ACL)은 어떻게 관리되나요?",
    faq_3_a: "git-ctx는 Keycloak OIDC 사용자의 계정 및 그룹 속성을 Bitbucket/GitLab ACL 권한과 동기화합니다. 검색 후보 생성 이전(Pre-filtering) 단계에서 사용자 권한을 검사하므로, 사용자가 소스 관리 시스템에서 접근 권한이 없는 저장소나 소스코드는 검색 결과 및 AI 토큰에 100% 노출되지 않습니다.",

    faq_4_q: "Q4. 인터넷이 없는 완전 폐쇄망(Air-Gapped) 환경도 지원되나요?",
    faq_4_a: "네, 완전히 지원합니다. git-ctx는 외부 Context7 서버나 인터넷 서비스에 전혀 의존하지 않으며, 오프라인 컨테이너 이미지 패키징, PostgreSQL / SQLite 지원, Kubernetes Kustomize 매니페스트 및 사내 로컬 LLM/임베딩 모델 호환 인터페이스를 제공합니다.",

    faq_5_q: "Q5. 어떤 AI 개발 도구(IDE)와 연동할 수 있나요?",
    faq_5_a: "Model Context Protocol(MCP) 표준을 준수하는 모든 클라이언트와 연동됩니다. Cursor, Antigravity CLI/IDE, Claude Desktop, VS Code (Continue/MCP extensions) 등 다양한 에이전트 환경에서 지원됩니다.",

    cta_title: "지금 git-ctx로 사내 AI 개발 환경을 혁신하세요",
    cta_desc: "보안 유출 걱정 없는 온프레미스 MCP 지식 플랫폼 git-ctx를 저장소에서 확인해 보세요.",
    cta_btn: "GitHub 저장소 바로가기",
    
    footer_desc: "사내 Bitbucket & GitLab을 위한 온프레미스 AI 개발 지식 플랫폼",
    footer_docs: "문서 및 가이드",
    footer_community: "커뮤니티 & 기여",
    footer_contact_label: "문의:"
  },
  en: {
    hero_pill: "On-Premise AI Dev Knowledge Platform v1.0",
    hero_title: "Connect Enterprise <span class=\"highlight\">Bitbucket & GitLab</span> Code to AI Agents in Real Time",
    hero_desc: "Two-stage hybrid retrieval built on the Model Context Protocol (MCP). Zero data leak via strict ACL authorization, security parsing, and AST code analysis for enterprise AI coding.",
    btn_quickstart: "Quick Start Guide",
    btn_github: "GitHub Repository",
    stat_mcp: "2-Stage MCP",
    stat_mcp_desc: "Context7 Compatible Protocol",
    stat_security: "Zero Data Leak",
    stat_security_desc: "Keycloak OIDC & Pre-filtering ACL",
    stat_ast: "AST Symbol Graph",
    stat_ast_desc: "Go, Java, TS, Py, SQL Parsing",
    stat_deploy: "On-Prem / Air-gapped",
    stat_deploy_desc: "Docker / K8s / Offline Ready",

    features_subtitle: "KEY FEATURES",
    features_title: "Core Features Built for Enterprise AI Coding",
    features_desc: "git-ctx provides the most accurate and safe knowledge context to AI models in isolated on-premise environments.",

    feat_1_title: "Strict Security & ACL Enforcement",
    feat_1_desc: "Keycloak OIDC and Bitbucket/GitLab ACLs are validated 100% before candidate generation. Unauthorized source code and docs are never exposed.",

    feat_2_title: "Context7 2-Stage MCP Workflow",
    feat_2_desc: "resolve-library-id → query-docs/search-code workflow drastically cuts token consumption while feeding precise context snippets to AI agents.",

    feat_3_title: "AST Code Structure & Symbol Graph",
    feat_3_desc: "Structural parsing for Go, Java, TypeScript, Python, and SQL powers find-symbol, trace-dependencies, and find-dependents impact analysis.",

    feat_4_title: "Hybrid BM25 + Vector Retrieval",
    feat_4_desc: "Combines PostgreSQL tsvector keyword search with on-premise OpenAI-compatible embedding. Automatic fallback to safe-lexical mode on model failure.",

    feat_5_title: "X-Ray Audit & Search Diagnostics",
    feat_5_desc: "Real-time MCP audit logs, step-by-step filtering evidence (Diagnostics), latency tracking, and explain-search-result ensure full observability.",

    feat_6_title: "Air-Gapped One-Click Deployment",
    feat_6_desc: "Provides offline image packaging, K8s Kustomize, Docker Compose, and SQLite/PostgreSQL dual fallback mode for zero-internet environments.",

    tools_subtitle: "MCP TOOL ECOSYSTEM",
    tools_title: "Comprehensive MCP Toolset for AI Agents",
    tools_desc: "Ready-to-use tools for Cursor, Claude Desktop, Antigravity, and all modern MCP-enabled developer assistants.",

    arch_subtitle: "ARCHITECTURE",
    arch_title: "Enterprise Hybrid Retrieval Pipeline",
    arch_desc: "From agent request to ACL authorization and 2-stage context refinement.",

    arch_step1_title: "AI Agent & IDE",
    arch_step1_desc: "MCP client requests from Cursor, Antigravity, Claude Desktop, etc.",
    arch_step2_title: "MCP /mcp Endpoint",
    arch_step2_desc: "Keycloak Bearer / API Key authentication & Streamable HTTP",
    arch_step3_title: "ACL & Source Adapter",
    arch_step3_desc: "Bitbucket 6.9.1 / GitLab ACL pre-filtering and incremental indexing",
    arch_step4_title: "Hybrid Retrieval",
    arch_step4_desc: "BM25 + Vector + Rerank and AST Symbol Graph context extraction",

    quickstart_subtitle: "QUICK START",
    quickstart_title: "Deploy git-ctx in Minutes",
    quickstart_desc: "Set just two environment variables to spin up the server and connect your MCP clients.",

    step1_title: "1. Start git-ctx Service (Docker or Go)",
    step1_desc: "Specify the long-term recovery key (GIT_CTX_RECOVERY_KEY) and DB DSN to start the server.",
    step2_title: "2. Verify Bootstrap Admin Token & SSO",
    step2_desc: "Log in with backups/bootstrap-admin.token generated on initial launch and configure Keycloak OIDC.",
    step3_title: "3. Configure MCP Client (e.g. claude_desktop_config.json)",
    step3_desc: "Register git-ctx MCP server to your AI agent using your generated MCP API Key.",

    faq_subtitle: "FAQ & AEO",
    faq_title: "Frequently Asked Questions",
    faq_desc: "Everything you need to know about adopting and running git-ctx.",

    faq_1_q: "Q1. What is git-ctx and why do I need it?",
    faq_1_a: "git-ctx is an on-premise developer knowledge platform that indexes Bitbucket Server 6.9.1 and GitLab code and documentation, exposing it to AI agents via Context7-compatible Model Context Protocol (MCP). It lets developer AI agents leverage enterprise code context in real time without external data leaks.",

    faq_2_q: "Q2. What is the benefit of the Context7 2-stage MCP workflow?",
    faq_2_a: "Instead of stuffing entire repositories into context windows, stage 1 (resolve-library-id) resolves the target repository/version, and stage 2 (query-docs / search-code) retrieves only the necessary code chunks. This cuts prompt token costs by up to 90% and improves agent precision.",

    faq_3_q: "Q3. How are code security and Access Control (ACL) protected?",
    faq_3_a: "git-ctx synchronizes Keycloak OIDC user accounts and roles with Bitbucket/GitLab ACLs. ACL validation occurs during pre-filtering before candidates are fetched, ensuring users never see or leak source code they lack permissions to access.",

    faq_4_q: "Q4. Is full air-gapped / offline deployment supported?",
    faq_4_a: "Yes, 100%. git-ctx operates entirely independent of external Context7 services or internet access. It supports offline container image packaging, PostgreSQL/SQLite, K8s Kustomize manifests, and local OpenAI-compatible embedding endpoints.",

    faq_5_q: "Q5. Which AI tools and IDEs are compatible?",
    faq_5_a: "git-ctx works with any client supporting the Model Context Protocol (MCP) standard, including Cursor, Antigravity CLI/IDE, Claude Desktop, and VS Code MCP extensions.",

    cta_title: "Transform Your Enterprise AI Development Today",
    cta_desc: "Explore git-ctx on GitHub for a secure, on-premise AI knowledge integration.",
    cta_btn: "Go to GitHub Repository",

    footer_desc: "On-Premise AI Dev Knowledge Platform for Bitbucket & GitLab",
    footer_docs: "Docs & Guides",
    footer_community: "Community & Contributing",
    footer_contact_label: "Contact:"
  }
};

let currentLang = 'ko';

function initLanguageSwitcher() {
  const langKoBtn = document.getElementById('lang-ko');
  const langEnBtn = document.getElementById('lang-en');

  if (!langKoBtn || !langEnBtn) return;

  langKoBtn.addEventListener('click', () => setLanguage('ko'));
  langEnBtn.addEventListener('click', () => setLanguage('en'));

  // Detect user preference or default to ko
  const savedLang = localStorage.getItem('git_ctx_lang');
  if (savedLang && (savedLang === 'ko' || savedLang === 'en')) {
    setLanguage(savedLang);
  } else {
    setLanguage('ko');
  }
}

function setLanguage(lang) {
  currentLang = lang;
  localStorage.setItem('git_ctx_lang', lang);

  const langKoBtn = document.getElementById('lang-ko');
  const langEnBtn = document.getElementById('lang-en');

  if (lang === 'ko') {
    langKoBtn.classList.add('active');
    langEnBtn.classList.remove('active');
    document.documentElement.lang = 'ko';
  } else {
    langEnBtn.classList.add('active');
    langKoBtn.classList.remove('active');
    document.documentElement.lang = 'en';
  }

  const dict = i18nData[lang];
  if (!dict) return;

  // Update elements with data-i18n
  document.querySelectorAll('[data-i18n]').forEach(el => {
    const key = el.getAttribute('data-i18n');
    if (dict[key]) {
      el.innerHTML = dict[key];
    }
  });
}

/* Tool Explorer Tabs */
function initToolExplorer() {
  const tabBtns = document.querySelectorAll('.tool-tab-btn');
  const tabDetails = document.querySelectorAll('.tool-detail');

  tabBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      const targetId = btn.getAttribute('data-target');

      tabBtns.forEach(b => b.classList.remove('active'));
      tabDetails.forEach(d => d.classList.remove('active'));

      btn.classList.add('active');
      const targetEl = document.getElementById(targetId);
      if (targetEl) {
        targetEl.classList.add('active');
      }
    });
  });
}

/* FAQ Accordion */
function initFaqAccordion() {
  const faqItems = document.querySelectorAll('.faq-item');

  faqItems.forEach(item => {
    const question = item.querySelector('.faq-question');
    if (!question) return;

    question.addEventListener('click', () => {
      const isActive = item.classList.contains('active');

      // Close all items
      faqItems.forEach(i => i.classList.remove('active'));

      // Toggle current item
      if (!isActive) {
        item.classList.add('active');
      }
    });
  });
}

/* Code Copy Buttons */
function initCopyButtons() {
  const copyBtns = document.querySelectorAll('.copy-btn');

  copyBtns.forEach(btn => {
    btn.addEventListener('click', async () => {
      const targetId = btn.getAttribute('data-code');
      const codeEl = document.getElementById(targetId);
      if (!codeEl) return;

      const codeText = codeEl.innerText || codeEl.textContent;

      try {
        await navigator.clipboard.writeText(codeText);
        const originalText = btn.innerHTML;
        btn.innerHTML = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="#10B981" stroke-width="2"><polyline points="20 6 9 17 4 12"></polyline></svg> Copied!`;
        setTimeout(() => {
          btn.innerHTML = originalText;
        }, 2000);
      } catch (err) {
        console.error('Failed to copy text: ', err);
      }
    });
  });
}

/* Mobile Nav Toggle */
function initMobileNav() {
  const toggleBtn = document.getElementById('mobile-nav-toggle');
  const navMenu = document.querySelector('.nav-menu');

  if (toggleBtn && navMenu) {
    toggleBtn.addEventListener('click', () => {
      if (navMenu.style.display === 'flex') {
        navMenu.style.display = 'none';
      } else {
        navMenu.style.display = 'flex';
        navMenu.style.flexDirection = 'column';
        navMenu.style.position = 'absolute';
        navMenu.style.top = '76px';
        navMenu.style.left = '0';
        navMenu.style.right = '0';
        navMenu.style.background = '#0C1019';
        navMenu.style.padding = '24px';
        navMenu.style.borderBottom = '1px solid var(--border-color)';
      }
    });
  }
}


  // Mobile Menu Toggle
  const mobileBtn = document.getElementById('mobile-menu-btn');
  const navLinks = document.querySelector('.nav-links') || document.querySelector('.nav-menu') || document.querySelector('.nav-list');
  if (mobileBtn && navLinks) {
    mobileBtn.addEventListener('click', () => {
      navLinks.classList.toggle('active');
    });
    navLinks.querySelectorAll('a').forEach(link => {
      link.addEventListener('click', () => navLinks.classList.remove('active'));
    });
  }
