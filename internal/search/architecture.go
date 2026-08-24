package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// What a repository imports says what it is. A service that imports net/http and
// database/sql is an HTTP service with a database behind it; one that imports a
// Kafka client is a consumer or a producer. That is already in the index, so the
// shape of a system can be read without parsing routes or annotations.
//
// This reports what the imports show and nothing more. It does not claim to know
// endpoint paths, topic names or which table a query touches: those live in
// string literals and framework annotations the indexer does not read, and
// guessing them would produce a diagram that looks authoritative and is wrong.

// Capability is a technology a component was observed using.
type Capability struct {
	// Name is the capability, for example "http-server" or "database".
	Name string
	// Evidence is the imports that led to it, so a reader can check the call.
	Evidence []string
}

// Component is one repository and what its imports say about it.
type Component struct {
	LibraryID, SourceType string
	Capabilities          []Capability
	// Imports is how many distinct external imports were examined.
	Imports int
}

// ServiceEdge is one repository referencing another. The reference is a symbol
// or module name that resolves to a second repository in the catalogue.
type ServiceEdge struct {
	From, To string
	// Via is what was imported or called to create the link.
	Via []string
	// Count is how many references support it, which separates a single stray
	// import from a real dependency.
	Count int
}

type ArchitectureMap struct {
	Components  []Component
	Edges       []ServiceEdge
	Diagnostics []string
}

// capabilitySignatures maps an import substring to what using it means. The
// substrings are deliberately distinctive: matching "sql" alone would classify
// anything mentioning it, so each entry names a package that is only imported by
// code doing the thing.
var capabilitySignatures = []struct {
	capability string
	markers    []string
}{
	{"http-server", []string{"net/http", "gin-gonic", "gorilla/mux", "labstack/echo", "go-chi/chi", "fiber",
		"springframework.web", "javax.servlet", "jakarta.servlet", "express", "@nestjs/common", "fastapi", "flask", "django.urls"}},
	{"http-client", []string{"net/http/httputil", "go-resty", "okhttp", "apache.http", "axios", "node-fetch", "requests", "httpx", "aiohttp"}},
	{"database", []string{"database/sql", "jackc/pgx", "go-sql-driver", "gorm.io", "jmoiron/sqlx", "mattn/go-sqlite3",
		"javax.persistence", "jakarta.persistence", "springframework.data", "hibernate", "jdbc",
		"sequelize", "typeorm", "prisma", "psycopg", "sqlalchemy", "pymysql"}},
	{"messaging", []string{"segmentio/kafka-go", "confluentinc/confluent-kafka", "IBM/sarama", "Shopify/sarama",
		"apache.kafka", "springframework.kafka", "amqp", "rabbitmq", "nats-io", "kafkajs", "aio_pika", "kombu", "celery"}},
	{"cache", []string{"redis", "memcache", "groupcache", "springframework.cache"}},
	{"rpc", []string{"google.golang.org/grpc", "grpc-java", "@grpc/grpc-js", "grpcio", "thrift"}},
	{"scheduler", []string{"robfig/cron", "quartz", "springframework.scheduling", "node-cron", "apscheduler", "celery.schedules"}},
	{"object-storage", []string{"aws-sdk", "minio-go", "cloud.google.com/go/storage", "azure-storage", "boto3"}},
	{"observability", []string{"opentelemetry", "prometheus/client_golang", "micrometer", "datadog", "sentry"}},
}

// classify returns the capabilities an import implies. One import can imply more
// than one, and an import matching nothing is simply not evidence.
func classify(target string) []string {
	lower := strings.ToLower(target)
	var out []string
	for _, signature := range capabilitySignatures {
		for _, marker := range signature.markers {
			if strings.Contains(lower, strings.ToLower(marker)) {
				out = append(out, signature.capability)
				break
			}
		}
	}
	return out
}

// componentState accumulates what one repository's dependencies say about it
// while the rows are scanned.
type componentState struct {
	sourceType   string
	capabilities map[string]map[string]bool
	imports      map[string]bool
}

// ArchitectureOverview reads the import and call graph and reports what each
// accessible repository appears to be, and which of them reference each other.
func (s *Service) ArchitectureOverview(ctx context.Context, principals []string, sourceType string, limit int) (ArchitectureMap, error) {
	if len(principals) == 0 {
		return ArchitectureMap{}, fmt.Errorf("no repository permissions are available for this caller")
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	join, predicate, args := repositoryACL(principals)
	statement := `SELECT r.library_id,r.source_type,d.target,d.dependency_kind
FROM code_dependencies d JOIN repositories r ON r.id=d.repository_id ` + join + `
WHERE r.enabled=1 AND ` + predicate
	if sourceType != "" {
		statement += ` AND r.source_type=?`
		args = append(args, sourceType)
	}
	// Imports are the architectural signal; calls are kept for the service edges
	// because a Go call to another module's symbol is a dependency too.
	statement += ` AND d.dependency_kind IN ('import','call')`

	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return ArchitectureMap{}, err
	}
	defer rows.Close()

	components := map[string]*componentState{}
	// references holds every target seen per library, so service edges can be
	// resolved after the catalogue of library names is known.
	references := map[string]map[string]int{}

	for rows.Next() {
		var libraryID, source, target, kind string
		if err = rows.Scan(&libraryID, &source, &target, &kind); err != nil {
			return ArchitectureMap{}, err
		}
		current := components[libraryID]
		if current == nil {
			current = &componentState{sourceType: source, capabilities: map[string]map[string]bool{}, imports: map[string]bool{}}
			components[libraryID] = current
		}
		if kind == "import" {
			current.imports[target] = true
			for _, capability := range classify(target) {
				if current.capabilities[capability] == nil {
					current.capabilities[capability] = map[string]bool{}
				}
				current.capabilities[capability][target] = true
			}
		}
		if references[libraryID] == nil {
			references[libraryID] = map[string]int{}
		}
		references[libraryID][target]++
	}
	if err = rows.Err(); err != nil {
		return ArchitectureMap{}, err
	}

	result := ArchitectureMap{}
	if len(components) == 0 {
		result.Diagnostics = append(result.Diagnostics,
			"접근 가능한 저장소에서 임포트 정보를 찾지 못했습니다. 아직 색인되지 않았거나, 심볼 그래프가 없는 언어일 수 있습니다.")
		return result, nil
	}
	for libraryID, state := range components {
		component := Component{LibraryID: libraryID, SourceType: state.sourceType, Imports: len(state.imports)}
		for capability, evidence := range state.capabilities {
			component.Capabilities = append(component.Capabilities, Capability{Name: capability, Evidence: sortedKeys(evidence, 3)})
		}
		sort.Slice(component.Capabilities, func(i, j int) bool {
			return component.Capabilities[i].Name < component.Capabilities[j].Name
		})
		result.Components = append(result.Components, component)
	}
	sort.Slice(result.Components, func(i, j int) bool {
		return result.Components[i].LibraryID < result.Components[j].LibraryID
	})
	if len(result.Components) > limit {
		result.Components = result.Components[:limit]
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("접근 가능한 저장소가 많아 %d곳만 표시했습니다.", limit))
	}
	result.Edges = resolveServiceEdges(components, references)
	return result, nil
}

// resolveServiceEdges links repositories that reference one another. A target is
// treated as naming another repository when it contains that repository's slug,
// which is how module paths and package names carry the repository name in every
// ecosystem here.
func resolveServiceEdges(components map[string]*componentState, references map[string]map[string]int) []ServiceEdge {
	slugs := map[string]string{}
	for libraryID := range components {
		parts := strings.Split(strings.Trim(libraryID, "/"), "/")
		slug := parts[len(parts)-1]
		// A one or two character slug matches far too much to be evidence.
		if len(slug) >= 3 {
			slugs[strings.ToLower(slug)] = libraryID
		}
	}
	type edgeKey struct{ from, to string }
	edges := map[edgeKey]*ServiceEdge{}
	for from, targets := range references {
		for target, count := range targets {
			lower := strings.ToLower(target)
			for slug, to := range slugs {
				if to == from || !strings.Contains(lower, slug) {
					continue
				}
				key := edgeKey{from, to}
				edge := edges[key]
				if edge == nil {
					edge = &ServiceEdge{From: from, To: to}
					edges[key] = edge
				}
				edge.Count += count
				if len(edge.Via) < 3 {
					edge.Via = append(edge.Via, target)
				}
			}
		}
	}
	out := make([]ServiceEdge, 0, len(edges))
	for _, edge := range edges {
		sort.Strings(edge.Via)
		out = append(out, *edge)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].From+out[i].To < out[j].From+out[j].To
	})
	return out
}

func sortedKeys(set map[string]bool, limit int) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FormatArchitecture renders the map, carrying the evidence for every claim and
// stating plainly what it did not look at. A diagram that hides its basis gets
// believed further than it should be.
func FormatArchitecture(result ArchitectureMap) string {
	var out strings.Builder
	out.WriteString("# 아키텍처 개요 (임포트 기준)\n\n")
	out.WriteString("> 각 저장소가 무엇을 임포트하는지로 추론했습니다. 엔드포인트 경로, 토픽 이름, 코드에 내장된 SQL은 색인하지 않으므로 여기에 없습니다.\n\n")

	if len(result.Components) > 0 {
		out.WriteString("## 구성요소\n\n")
		for _, component := range result.Components {
			fmt.Fprintf(&out, "### `%s` (%s)\n\n", component.LibraryID, component.SourceType)
			if len(component.Capabilities) == 0 {
				fmt.Fprintf(&out, "임포트 %d건에서 알려진 기술 신호를 찾지 못했습니다.\n\n", component.Imports)
				continue
			}
			for _, capability := range component.Capabilities {
				fmt.Fprintf(&out, "- **%s** — %s\n", capability.Name, strings.Join(capability.Evidence, ", "))
			}
			out.WriteString("\n")
		}
	}

	if len(result.Edges) > 0 {
		out.WriteString("## 저장소 간 참조\n\n")
		for _, edge := range result.Edges {
			fmt.Fprintf(&out, "- `%s` → `%s` (참조 %d건: %s)\n", edge.From, edge.To, edge.Count, strings.Join(edge.Via, ", "))
		}
		out.WriteString("\n")
	} else if len(result.Components) > 1 {
		out.WriteString("## 저장소 간 참조\n\n서로를 참조하는 흔적을 찾지 못했습니다. 모듈 경로에 저장소 이름이 드러나지 않는 구성일 수 있습니다.\n\n")
	}

	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(&out, "_%s_\n", diagnostic)
	}
	return strings.TrimSpace(out.String()) + "\n"
}
