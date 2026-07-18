'use client';

import { KubeQueueClient } from '@kubequeue/api-client';
import { useRouter } from 'next/navigation';
import { type FormEvent, useState } from 'react';

const client = new KubeQueueClient();
const initialTemplate = `{
  "apiVersion": "batch/v1",
  "kind": "Job",
  "metadata": { "name": "hello-batch" },
  "spec": {
    "template": {
      "spec": {
        "restartPolicy": "Never",
        "containers": [{
          "name": "job",
          "image": "busybox:1.36",
          "command": ["sh", "-c", "echo hello && sleep 10"]
        }]
      }
    }
  }
}`;

export function JobForm() {
  const router = useRouter();
  const [template, setTemplate] = useState(initialTemplate);
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError('');
    const data = new FormData(event.currentTarget);
    try {
      const parsed = JSON.parse(template) as Record<string, unknown>;
      const scheduledFor = String(data.get('scheduledFor'));
      const job = await client.createJob({
        name: String(data.get('name')),
        namespace: String(data.get('namespace')),
        team: String(data.get('team')) || undefined,
        priority: Number(data.get('priority')),
        scheduledFor: scheduledFor ? new Date(scheduledFor).toISOString() : undefined,
        template: parsed,
      });
      router.push(`/jobs/${job.id}`);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to submit Job');
      setSubmitting(false);
    }
  }

  return (
    <main className="page-shell narrow">
      <div className="page-title">
        <p className="eyebrow">New workload</p>
        <h1>Submit a Kubernetes Job</h1>
        <p>KubeQueue stores this intent, then admits the Job when capacity is available.</p>
      </div>
      <form className="job-form panel" onSubmit={(event) => void submit(event)}>
        <div className="form-grid">
          <label>
            <span>Name</span>
            <input name="name" required defaultValue="hello-batch" />
          </label>
          <label>
            <span>Namespace</span>
            <input name="namespace" required defaultValue="default" />
          </label>
          <label>
            <span>Team label</span>
            <input name="team" placeholder="data-platform" />
          </label>
          <label>
            <span>Priority</span>
            <input name="priority" type="number" min="-1000" max="1000" defaultValue="0" />
          </label>
          <label className="wide">
            <span>Do not start before</span>
            <input name="scheduledFor" type="datetime-local" />
          </label>
          <label className="wide">
            <span>Job manifest (JSON)</span>
            <textarea
              spellCheck={false}
              rows={22}
              value={template}
              onChange={(event) => setTemplate(event.target.value)}
              aria-describedby="manifest-help"
            />
            <small id="manifest-help">
              A standard batch/v1 Job. KubeQueue adds its management label.
            </small>
          </label>
        </div>
        {error ? (
          <div className="alert" role="alert">
            {error}
          </div>
        ) : null}
        <div className="form-actions">
          <button className="button ghost" type="button" onClick={() => router.back()}>
            Cancel
          </button>
          <button className="button primary" disabled={submitting} type="submit">
            {submitting ? 'Submitting…' : 'Add to queue'}
          </button>
        </div>
      </form>
    </main>
  );
}
