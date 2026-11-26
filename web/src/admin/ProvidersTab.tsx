// SPDX-License-Identifier: GPL-2.0-or-later
import { API_KEY_LINKS } from "../lib/keylinks";
import { useEffect, useState, type FormEvent } from "react";
import { del, errText, post, put } from "../lib/api";
import {
  Badge,
  Banner,
  Button,
  Card,
  Input,
  Modal,
  Select,
  Skeleton,
  Toggle,
  useToast,
} from "../ui/ui";
import { useLoad } from "./shared";

export interface Prov {
  id: number;
  name: string;
  kind: string;
  base_url: string;
  concurrency: number;
  enabled: boolean;
  has_key: boolean;
  stream_probe: { ok?: boolean; detail?: string };
  helper_model: string;
  helper_assert: boolean;
}

export interface ModFull {
  id: number;
  provider_id: number;
  name: string;
  enabled: boolean;
  tools: number;
  recommended: boolean;
  context_override: number;
  tool_probe: { ok?: boolean; detail?: string };
}
export interface Bench {
  id: number;
  model_id: number;
  time: string;
  report: {
    pass_rate: number;
    tokens_per_sec: number;
    ttft_ms: number;
    tool_call_failure_rate: number;
    tasks: { id: string; passed: boolean; detail: string; seconds: number }[];
  };
}

/** Where an admin creates an API key for a hosted provider. */

/** What is known about a model, as short labels for the Model column. */
export function modelFeatures(
  m: ModFull,
  p: Pick<Prov, "stream_probe">,
): { text: string; tone: "success" | "warn" | "info" | "neutral"; title?: string }[] {
  const out: { text: string; tone: "success" | "warn" | "info" | "neutral"; title?: string }[] = [];
  if (m.tools === 0)
    out.push({
      text: "no tools",
      tone: "warn",
      title: "The provider says this model does not support tool calls.",
    });
  else if (m.tools === 1)
    out.push({
      text: "tools",
      tone: "success",
      title: "The provider says this model supports tool calls.",
    });
  if (m.tool_probe.ok === true)
    out.push({
      text: "calls tools",
      tone: "success",
      title: m.tool_probe.detail ?? "A live test made a tool call.",
    });
  else if (m.tool_probe.ok === false)
    out.push({
      text: "does not call tools",
      tone: "warn",
      title: m.tool_probe.detail ?? "A live test got no tool call.",
    });
  else
    out.push({
      text: "not probed",
      tone: "neutral",
      title: "Run Probe to test tool calls with this model.",
    });
  if (p.stream_probe.ok === false)
    out.push({ text: "stream is buffered", tone: "warn", title: p.stream_probe.detail });
  if (m.context_override > 0)
    out.push({
      text: `context ${m.context_override}`,
      tone: "info",
      title: "The context window is set by hand.",
    });
  return out;
}

interface UserProvider {
  id: number;
  user: string;
  name: string;
  base_url: string;
  models: number;
  models_on: number;
}

/** Hosted services users brought with their own keys: names and addresses, never the keys. */
function UserProviders() {
  const toast = useToast();
  const list = useLoad<UserProvider[]>("/user-providers");
  if (!list.data || list.data.length === 0) return null;
  return (
    <Card title={`Providers added by users (${list.data.length})`}>
      <p className="muted">
        Each is a hosted service with the user&rsquo;s own key; prompts sent to it leave this
        machine. Removing one stops sessions using it.
      </p>
      <ul>
        {list.data.map((p) => (
          <li key={p.id} className="row">
            <span className="grow">
              <strong>{p.name}</strong> <span className="muted">{p.base_url}</span>
              <br />
              <span className="muted">
                {p.user} · {p.models_on} of {p.models} models on
              </span>
            </span>
            <Button
              variant="danger"
              onClick={async () => {
                try {
                  await del(`/user-providers/${p.id}`);
                  list.reload();
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            >
              Remove
            </Button>
          </li>
        ))}
      </ul>
    </Card>
  );
}

export function ProvidersTab() {
  const toast = useToast();
  const provs = useLoad<Prov[]>("/providers");
  const models = useLoad<ModFull[]>("/models");
  const bench = useLoad<Bench[]>("/benchmarks");
  const [adding, setAdding] = useState(false);
  const [f, setF] = useState({
    name: "",
    kind: "ollama",
    base_url: "",
    api_key: "",
    concurrency: "1",
  });
  async function add(e: FormEvent) {
    e.preventDefault();
    try {
      await post("/providers", { ...f, concurrency: Number(f.concurrency) });
      setF({ ...f, name: "", base_url: "", api_key: "" });
      setAdding(false);
      provs.reload();
    } catch (x) {
      toast(errText(x), "error");
    }
  }
  if (!provs.data || !models.data) return <Skeleton />;
  const latest = (id: number) => bench.data?.find((b) => b.model_id === id);
  return (
    <div className="stack">
      <div className="row">
        <p className="muted grow">
          Syntaxis never downloads or deletes models on your LLM server. It can make the server load
          a model, simply by asking for it by name when a session, probe or benchmark uses it.
        </p>
        <Button variant="primary" onClick={() => setAdding(true)}>
          Add provider
        </Button>
      </div>
      {provs.data.length === 0 && (
        <Banner>
          No provider yet. Add your Ollama, llama.cpp, LiteLLM or OpenAI-compatible server.
        </Banner>
      )}
      {provs.data.map((p) => (
        <Card key={p.id} title={`${p.name} (${p.kind})`}>
          <p className="muted">
            {p.base_url} · concurrency {p.concurrency} {p.has_key && "· key stored"}
          </p>
          <Select
            label="Helper model for cheap jobs (titles, summaries)"
            value={p.helper_model}
            onChange={async (e) => {
              try {
                const r = await put<{ notice?: string }>(`/providers/${p.id}/helper`, {
                  model: e.target.value,
                  assert_resident: p.helper_assert,
                });
                toast(r.notice ?? "Saved.", r.notice ? "info" : "success");
                provs.reload();
              } catch (x) {
                toast(errText(x), "error");
              }
            }}
            options={[
              { value: "", label: "None (the session's own model does everything)" },
              ...(models.data ?? [])
                .filter((m) => m.provider_id === p.id)
                .map((m) => ({ value: m.name, label: m.name })),
            ]}
          />
          <p className="muted">
            Used only while the server reports it is already loaded: Syntaxis does not ask the
            server to load the helper just for these jobs. If it is not loaded, or fails, the
            session&apos;s own model does the job.
          </p>
          <div className="row">
            <Button
              onClick={async () => {
                try {
                  const r = await post<{ models: number }>(`/providers/${p.id}/refresh`);
                  toast(`Found ${r.models} models.`, "success");
                  models.reload();
                } catch (x) {
                  toast(errText(x), "error");
                }
              }}
            >
              Discover models
            </Button>
            <Button
              variant="danger"
              onClick={async () => {
                await del(`/providers/${p.id}`);
                provs.reload();
                models.reload();
              }}
            >
              Remove
            </Button>
          </div>
          <div className="table-wrap" tabIndex={0} role="region" aria-label={`Models of ${p.name}`}>
            <table>
              <thead>
                <tr>
                  <th>Model</th>
                  <th>Enabled</th>
                  <th>Recommended</th>
                  <th>Context override</th>
                  <th>Last benchmark</th>
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {models
                  .data!.filter((m) => m.provider_id === p.id)
                  .map((m) => {
                    const b = latest(m.id);
                    return (
                      <tr key={m.id}>
                        <td>
                          {m.name}
                          <div className="pills">
                            {modelFeatures(m, p).map((f) => (
                              <span key={f.text} title={f.title}>
                                <Badge tone={f.tone}>{f.text}</Badge>
                              </span>
                            ))}
                          </div>
                        </td>
                        <td>
                          <Toggle
                            label={`Enable ${m.name}`}
                            hideLabel
                            checked={m.enabled}
                            onChange={async (v) => {
                              await post(`/models/${m.id}/enabled`, { enabled: v });
                              models.reload();
                            }}
                          />
                        </td>
                        <td>
                          <Toggle
                            label={`Recommend ${m.name} for agents`}
                            hideLabel
                            checked={m.recommended}
                            onChange={async (v) => {
                              await post(`/models/${m.id}/recommended`, { recommended: v });
                              models.reload();
                            }}
                          />
                        </td>
                        <td>{m.context_override || <span className="muted">detected</span>}</td>
                        <td>
                          {b ? (
                            `${Math.round(b.report.pass_rate * 100)}% · ${b.report.tokens_per_sec.toFixed(1)} tok/s`
                          ) : (
                            <span className="muted">never</span>
                          )}
                        </td>
                        <td>
                          <Button
                            onClick={async () => {
                              try {
                                await post(`/models/${m.id}/probe`);
                                toast(
                                  "Probe started. It may load the model, so it runs in the background.",
                                  "success",
                                );
                                setTimeout(() => {
                                  models.reload();
                                  provs.reload();
                                }, 30000);
                              } catch (x) {
                                toast(errText(x), "error");
                              }
                            }}
                          >
                            Probe
                          </Button>{" "}
                          <Button
                            onClick={async () => {
                              try {
                                await post(`/models/${m.id}/benchmark`);
                                toast(
                                  "Benchmark started. It runs in the background at the lowest priority; follow it on the Benchmarks screen.",
                                  "success",
                                );
                                setTimeout(bench.reload, 15000);
                              } catch (x) {
                                toast(errText(x), "error");
                              }
                            }}
                          >
                            Benchmark
                          </Button>
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
        </Card>
      ))}
      <UserProviders />
      {adding && (
        <Modal
          title="Add provider"
          onClose={() => setAdding(false)}
          actions={
            <>
              <Button onClick={() => setAdding(false)}>Cancel</Button>
              <Button variant="primary" type="submit" form="provider-form">
                Add
              </Button>
            </>
          }
        >
          <form id="provider-form" onSubmit={add}>
            <Input
              label="Name"
              placeholder="workstation-ollama"
              value={f.name}
              onChange={(e) => setF({ ...f, name: e.target.value })}
              required
            />
            <Select
              label="Kind"
              value={f.kind}
              onChange={(e) => setF({ ...f, kind: e.target.value })}
              options={[
                { value: "ollama", label: "Ollama" },
                { value: "llamacpp", label: "llama.cpp server" },
                { value: "litellm", label: "LiteLLM" },
                { value: "openai", label: "OpenAI-compatible" },
              ]}
            />
            <Input
              label="Base URL"
              placeholder="http://llm.internal:11434"
              value={f.base_url}
              onChange={(e) => setF({ ...f, base_url: e.target.value })}
              required
            />
            <Input
              label="API key (optional)"
              type="password"
              value={f.api_key}
              onChange={(e) => setF({ ...f, api_key: e.target.value })}
            />
            <details className="muted" style={{ margin: "var(--s-3, 12px) 0" }}>
              <summary>Where do I get an API key for a hosted provider?</summary>
              <p>
                Local servers (Ollama, llama.cpp) need no key. For a hosted service choose
                &ldquo;OpenAI-compatible&rdquo;, use the base URL from the provider&rsquo;s
                documentation, and create a key on its site:
              </p>
              <ul>
                {API_KEY_LINKS.map((l) => (
                  <li key={l.name}>
                    <a href={l.url} target="_blank" rel="noreferrer noopener">
                      {l.name}
                    </a>
                  </li>
                ))}
              </ul>
            </details>
            <Input
              label="Concurrency"
              placeholder="2"
              hint="Match your server's parallel setting; Syntaxis never exceeds it."
              value={f.concurrency}
              onChange={(e) => setF({ ...f, concurrency: e.target.value })}
            />
          </form>
        </Modal>
      )}
    </div>
  );
}

export interface BenchRun {
  model_id: number;
  state: "running" | "failed";
  started: string;
  message?: string;
}

export function BenchmarksTab() {
  const { data, reload } = useLoad<Bench[]>("/benchmarks");
  const models = useLoad<ModFull[]>("/models");
  const { data: runList, reload: reloadRuns } = useLoad<BenchRun[]>("/benchmarks/runs");
  const running = (runList ?? []).some((r) => r.state === "running");
  // A run takes minutes: look again until none is running, then show the new result.
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => {
      reloadRuns();
      reload();
    }, 5000);
    return () => clearInterval(t);
  }, [running, reloadRuns, reload]);
  if (!data || !models.data || !runList) return <Skeleton />;
  const name = (id: number) => models.data!.find((m) => m.id === id)?.name ?? `model ${id}`;
  return (
    <div className="stack">
      {runList.map((r) =>
        r.state === "running" ? (
          <Banner key={r.model_id} tone="info">
            Benchmark of {name(r.model_id)} is running (started{" "}
            {new Date(r.started).toLocaleTimeString()}). It takes several minutes and the result
            appears here when it finishes.
          </Banner>
        ) : (
          <Banner key={r.model_id} tone="danger">
            Benchmark of {name(r.model_id)} did not run: {r.message}
          </Banner>
        ),
      )}
      {data.length === 0 && runList.length === 0 && (
        <Banner>
          No benchmark has been run. Start one from Providers &amp; models. Results are visible to
          admins only.
        </Banner>
      )}
      {data.map((b) => (
        <Card
          key={b.id}
          title={`${name(b.model_id)} — ${Math.round(b.report.pass_rate * 100)}% passed`}
        >
          <p className="muted">
            {new Date(b.time).toLocaleString()} · {b.report.tokens_per_sec.toFixed(1)} tok/s · first
            token {Math.round(b.report.ttft_ms)} ms · tool failures{" "}
            {Math.round(b.report.tool_call_failure_rate * 100)}%
          </p>
          <ul>
            {b.report.tasks.map((t) => (
              <li key={t.id}>
                <Badge tone={t.passed ? "success" : "danger"}>{t.passed ? "pass" : "fail"}</Badge>{" "}
                <strong>{t.id}</strong>{" "}
                <span className="muted">
                  {t.seconds.toFixed(0)}s — {t.detail}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      ))}
    </div>
  );
}
