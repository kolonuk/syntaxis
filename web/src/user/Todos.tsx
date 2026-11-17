// SPDX-License-Identifier: GPL-2.0-or-later
import { progress, type Todo } from "../lib/todos";

const MARK: Record<Todo["status"], string> = { completed: "✓", in_progress: "▸", pending: "○" };
const WORD: Record<Todo["status"], string> = {
  completed: "done",
  in_progress: "in progress",
  pending: "to do",
};

/** The agent's own task list, as a progress list above the message box. */
export function TodoList({ todos }: { todos: Todo[] }) {
  if (todos.length === 0) return null;
  const { done, total } = progress(todos);
  const current = todos.find((t) => t.status === "in_progress");
  return (
    <details className="todos" open={done < total}>
      <summary>
        <span className="todos-count">
          Progress {done} of {total}
        </span>
        {current && <span className="muted todos-now"> · {current.content}</span>}
      </summary>
      <div
        className="todos-bar"
        role="progressbar"
        aria-label="Task progress"
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={done}
      >
        <span style={{ width: `${(done / total) * 100}%` }} />
      </div>
      <ul>
        {todos.map((t, i) => (
          <li key={i} className={`todo ${t.status}`}>
            <span aria-hidden="true">{MARK[t.status]}</span>
            <span className="sr-only">{WORD[t.status]}: </span>
            {t.content}
          </li>
        ))}
      </ul>
    </details>
  );
}
