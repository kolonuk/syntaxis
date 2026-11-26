// SPDX-License-Identifier: GPL-2.0-or-later
import { useEffect, useState, type FormEvent } from "react";
import { del, errText, get, post, put } from "../lib/api";
import { API_KEY_LINKS } from "../lib/keylinks";
import { Banner, Button, Card, Input, Modal, Skeleton, Toggle, useToast } from "../ui/ui";

interface MyModel {
  id: number;
  name: string;
  enabled: boolean;
  context: number;
}
interface MyProvider {
  id: number;
  name: string;
  base_url: string;
  concurrency: number;
  has_key: boolean;
  models: MyModel[];
}
interface Mine {
  allowed: boolean;
  max: number;
  providers: MyProvider[];
}

/**
 * Hosted model services the user brings with their own API key (an OpenAI-compatible API). The key
 * is stored encrypted and never shown again. What is sent to such a service leaves this machine.
 */
export function MyProviders() {
  const toast = useToast();
  const [data, setData] = useState<Mine | null>(null);
  // the provider being added (id 0) or changed, in the pop-up
  const [edit, setEdit] = useState<{ id: number; name: string; base_url: string } | null>(null);
  const [key, setKey] = useState("");
  const [models, setModels] = useState<number | null>(null);
  const [removing, setRemoving] = useState<MyProvider | null>(null);
  const [busy, setBusy] = useState(false);
  const load = () =>
    get<Mine>("/my/providers").then((d) => setData({ ...d, providers: d.providers ?? [] }));
  useEffect(() => {
    void load();
  }, []);

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!edit) return;
    setBusy(true);
    try {
      const body = { name: edit.name, base_url: edit.base_url, api_key: key };
      if (edit.id) await put(`/my/providers/${edit.id}`, body);
      else await post("/my/providers", body);
      setEdit(null);
      setKey("");
      toast(
        edit.id ? "Provider saved." : "Provider added. Turn on the models you want to use.",
        "success",
      );
      void load();
    } catch (x) {
      toast(errText(x), "error");
    } finally {
      setBusy(false);
    }
  }
  async function patchModel(
    p: MyProvider,
    m: MyModel,
    body: { enabled?: boolean; context?: number },
  ) {
    try {
      await put(`/my/providers/${p.id}/models/${m.id}`, body);
      void load();
    } catch (x) {
      toast(errText(x), "error");
    }
  }

  if (!data) return <Skeleton lines={3} />;
  const open = data.providers.find((p) => p.id === models) ?? null;
  return (
    <Card title="My model providers">
      <p className="muted">
        Add a hosted model service with your own API key. What you send to it leaves this machine,
        and it is billed to you by that service. Your key is stored encrypted and is never shown
        again. Only you can see or use it.
      </p>
      {!data.allowed ? (
        <Banner>An admin has not turned on providers of your own.</Banner>
      ) : (
        <>
          {data.providers.length === 0 ? (
            <p className="muted">No providers yet.</p>
          ) : (
            <ul className="list-rows">
              {data.providers.map((p) => (
                <li key={p.id}>
                  <span className="grow">
                    <strong>{p.name}</strong> <span className="muted">{p.base_url}</span>
                    <br />
                    <span className="muted">
                      {p.models.filter((m) => m.enabled).length} of {p.models.length} models on
                    </span>
                  </span>
                  <Button variant="ghost" onClick={() => setModels(p.id)}>
                    Models
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => setEdit({ id: p.id, name: p.name, base_url: p.base_url })}
                  >
                    Edit
                  </Button>
                  <Button variant="danger" onClick={() => setRemoving(p)}>
                    Remove
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {data.providers.length < data.max && (
            <Button variant="primary" onClick={() => setEdit({ id: 0, name: "", base_url: "" })}>
              Add a provider
            </Button>
          )}
        </>
      )}
      {edit && (
        <Modal
          title={edit.id ? `Edit ${edit.name}` : "Add a provider"}
          onClose={() => setEdit(null)}
          actions={
            <>
              <Button onClick={() => setEdit(null)}>Cancel</Button>
              <Button variant="primary" type="submit" form="my-provider-form" disabled={busy}>
                {busy ? "Checking…" : "Check and save"}
              </Button>
            </>
          }
        >
          <form id="my-provider-form" className="stack" onSubmit={save}>
            <Input
              label="Name"
              placeholder="hosted"
              value={edit.name}
              onChange={(e) => setEdit({ ...edit, name: e.target.value })}
              required
            />
            <Input
              label="Address"
              placeholder="https://api.example.com/v1"
              hint="The address from the service's documentation. It must be https and public."
              value={edit.base_url}
              onChange={(e) => setEdit({ ...edit, base_url: e.target.value })}
              required
            />
            <Input
              label={edit.id ? "API key (leave empty to keep the stored one)" : "API key"}
              type="password"
              autoComplete="off"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              required={!edit.id}
            />
            <details className="muted">
              <summary>Where do I get an API key?</summary>
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
          </form>
        </Modal>
      )}
      {open && (
        <Modal
          title={`Models of ${open.name}`}
          onClose={() => setModels(null)}
          actions={
            <>
              <Button
                onClick={async () => {
                  try {
                    await post(`/my/providers/${open.id}/refresh`, {});
                    void load();
                  } catch (x) {
                    toast(errText(x), "error");
                  }
                }}
              >
                Refresh the list
              </Button>
              <Button variant="primary" onClick={() => setModels(null)}>
                Done
              </Button>
            </>
          }
        >
          <p className="muted">
            Turn on the models you want in the new-session list. The window is how much text the
            model can read at once.
          </p>
          <ul className="list-rows">
            {open.models.map((m) => (
              <li key={m.id}>
                <Toggle
                  label={m.name}
                  checked={m.enabled}
                  onChange={(v) => void patchModel(open, m, { enabled: v })}
                />
                <span className="grow" />
                <input
                  className="input"
                  style={{ width: 110 }}
                  aria-label={`Window of ${m.name} (tokens)`}
                  defaultValue={m.context}
                  inputMode="numeric"
                  onBlur={(e) => {
                    const n = Number(e.target.value);
                    if (n && n !== m.context) void patchModel(open, m, { context: n });
                  }}
                />
              </li>
            ))}
          </ul>
        </Modal>
      )}
      {removing && (
        <Modal
          title={`Remove ${removing.name}?`}
          onClose={() => setRemoving(null)}
          actions={
            <>
              <Button onClick={() => setRemoving(null)}>Cancel</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await del(`/my/providers/${removing.id}`);
                    setRemoving(null);
                    void load();
                  } catch (x) {
                    toast(errText(x), "error");
                  }
                }}
              >
                Remove
              </Button>
            </>
          }
        >
          <p>
            Your key is deleted from Syntaxis. Sessions running on this provider stop; you can add
            it again later with the key.
          </p>
        </Modal>
      )}
    </Card>
  );
}
