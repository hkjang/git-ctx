CREATE TABLE IF NOT EXISTS quality_benchmark_cases (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  library_id TEXT NOT NULL,
  query TEXT NOT NULL,
  principals_json TEXT NOT NULL DEFAULT '[]',
  relevant_sources_json TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_by TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS quality_benchmark_runs (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  top_k INTEGER NOT NULL,
  case_count INTEGER NOT NULL DEFAULT 0,
  passed_count INTEGER NOT NULL DEFAULT 0,
  recall_at_k REAL NOT NULL DEFAULT 0,
  mrr REAL NOT NULL DEFAULT 0,
  ndcg_at_k REAL NOT NULL DEFAULT 0,
  minimum_recall REAL NOT NULL DEFAULT 0,
  minimum_mrr REAL NOT NULL DEFAULT 0,
  minimum_ndcg REAL NOT NULL DEFAULT 0,
  created_by TEXT NOT NULL,
  error_message TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at TIMESTAMP
);
CREATE TABLE IF NOT EXISTS quality_benchmark_results (
  run_id TEXT NOT NULL REFERENCES quality_benchmark_runs(id) ON DELETE CASCADE,
  case_id TEXT NOT NULL REFERENCES quality_benchmark_cases(id),
  retrieved_sources_json TEXT NOT NULL DEFAULT '[]',
  recall_at_k REAL NOT NULL DEFAULT 0,
  reciprocal_rank REAL NOT NULL DEFAULT 0,
  ndcg_at_k REAL NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(run_id, case_id)
);
CREATE INDEX IF NOT EXISTS idx_quality_runs_created ON quality_benchmark_runs(created_at);
