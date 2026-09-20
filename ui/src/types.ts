export type Task = {
  id: string;
  role: string;
  phase: string;
  criteria: string[];
  depends_on: string[];
  write_paths: string[];
  objective: string;
};
export type Job = {
  task_id: string;
  status: string;
  acceptance: string;
  actual_model?: string;
  objective?: string;
};
export type Workflow = {
  available: boolean;
  changeID?: string;
  phase?: string;
  error?: string;
  changes: { id: string; title?: string; phase: string; archived?: boolean }[];
  tasks?: Task[];
  jobs?: Job[];
  cycle?: { label: string; status: string }[];
};
export type State = {
  project: string;
  directory: string;
  branch: string;
  session: string;
  model: string;
  initialized: boolean;
  conversation?: { role: string; content: string }[];
  workflow?: Workflow;
  documents?: Record<string, string>;
  contract?: {
    title?: string;
    contract_digest: string;
    approval_current: boolean;
    phase: string;
    updated_at?: string;
    check?: {
      report?: { criteria: { id: string; status: string; evidence: string }[] };
    };
  };
  workflowError?: string;
  contractError?: string;
};
export type Prompt = {
  id: string;
  kind: string;
  title: string;
  detail?: string;
  choices?: string[];
};
export type Message = { role: string; text: string };
export type ProviderData = {
  providers: {
    name: string;
    type: string;
    key_set: boolean;
    requires_key: boolean;
    models: number;
  }[];
  accounts: { id: string; label: string; authenticated: boolean }[];
  models: string[];
};
export type UIEvent = { event: string; data: any };
export interface Client {
  request<T = any>(op: string, args?: Record<string, unknown>): Promise<T>;
  subscribe(listener: (event: UIEvent) => void): () => void;
  close(): void;
}
