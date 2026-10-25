DROP TABLE IF EXISTS code_wiki_model_slots;
DROP TABLE IF EXISTS code_wiki_topics;
ALTER TABLE code_wiki_run_events DROP COLUMN step_id;
ALTER TABLE code_wiki_run_events DROP COLUMN attempt_id;
ALTER TABLE code_wiki_run_events DROP COLUMN topic_key;
ALTER TABLE code_wiki_runs DROP COLUMN topic_concurrency;
ALTER TABLE code_wiki_runs DROP COLUMN execution_deadline;
ALTER TABLE code_wiki_runs DROP COLUMN model_call_limit;
ALTER TABLE code_wiki_runs DROP COLUMN execution_epoch;
