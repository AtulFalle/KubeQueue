'use client';

import type { AuditEvent } from '@kubequeue/api-client';
import { useEffect, useRef, type KeyboardEvent } from 'react';

export function AuditEventDialog({
  auditEvent,
  returnFocus,
  onClose,
}: {
  auditEvent: AuditEvent;
  returnFocus: HTMLButtonElement | null;
  onClose: () => void;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    closeButton.current?.focus();
    return () => returnFocus?.focus();
  }, [returnFocus]);

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape') {
      event.preventDefault();
      onClose();
    } else if (event.key === 'Tab') {
      event.preventDefault();
      closeButton.current?.focus();
    }
  }

  return (
    <div className="audit-dialog-backdrop">
      <div
        className="surface audit-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="audit-detail-title"
        onKeyDown={handleKeyDown}
      >
        <div className="audit-dialog-heading">
          <div>
            <p className="eyebrow">Audit event</p>
            <h2 id="audit-detail-title">{auditEvent.action}</h2>
          </div>
          <button ref={closeButton} className="button ghost" type="button" onClick={onClose}>
            Close details
          </button>
        </div>
        <dl className="audit-detail-list">
          <Fact label="Event ID" value={auditEvent.id} />
          <Fact label="Occurred" value={formatDate(auditEvent.occurredAt)} />
          <Fact label="Actor" value={auditEvent.actor.principalId} />
          <Fact label="Authentication" value={auditEvent.actor.authenticationMethod} />
          <Fact label="Target" value={`${auditEvent.target.type} · ${auditEvent.target.id}`} />
          <Fact label="Project" value={auditEvent.scope.projectId ?? 'Installation scope'} />
          <Fact label="Decision" value={auditEvent.decision} />
          <Fact label="Result" value={auditEvent.result} />
          <Fact label="Reason" value={auditEvent.reason} />
          <Fact label="Request ID" value={auditEvent.requestId} />
          <Fact label="Trace ID" value={auditEvent.traceId} />
          <Fact
            label="Source"
            value={`${auditEvent.source.provenance} · ${auditEvent.source.address}`}
          />
        </dl>
        <div className="audit-summaries">
          <AuditSummary title="Before summary" summary={auditEvent.before} />
          <AuditSummary title="After summary" summary={auditEvent.after} />
        </div>
      </div>
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function AuditSummary({ title, summary }: { title: string; summary: AuditEvent['before'] }) {
  return (
    <section aria-label={title}>
      <h3>{title}</h3>
      {summary ? (
        <dl>
          <Fact label="State" value={summary.state ?? 'Not recorded'} />
          <Fact label="Changed fields" value={summary.changedFields.join(', ') || 'None'} />
          <Fact label="Redactions" value={String(summary.redactionCount)} />
          <Fact label="Truncated" value={summary.truncated ? 'Yes' : 'No'} />
        </dl>
      ) : (
        <p>Not recorded.</p>
      )}
    </section>
  );
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'medium',
  }).format(new Date(value));
}
