import { KubeQueueClient } from '@kubequeue/api-client';
import type { Metadata } from 'next';
import Link from 'next/link';
import type { ReactNode } from 'react';

import { SystemIndicator } from '../components/system-indicator';
import { getBFFConfig } from '../lib/bff-config';
import { currentBrowserSession, sessionCredential } from '../lib/server-session';
import './styles.css';

export const metadata: Metadata = {
  title: 'KubeQueue',
  description: 'Lightweight control and visibility for Kubernetes batch jobs.',
};

export default async function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  const session = await currentBrowserSession().catch(() => undefined);
  const credential = session ? await sessionCredential().catch(() => undefined) : undefined;
  const access = credential
    ? await new KubeQueueClient(`${getBFFConfig().apiOrigin}/api/v1`, credential, 'Session')
        .getCurrentAccess()
        .catch(() => undefined)
    : undefined;
  const canReadAudit = access?.permissions.some(({ permission }) => permission === 'audit.read');
  const canReadSupport = access?.permissions.some(
    ({ permission }) => permission === 'support.diagnostics.read',
  );
  return (
    <html lang="en">
      <head>{session ? <meta name="kubequeue-csrf" content={session.csrfToken} /> : null}</head>
      <body>
        <a className="skip-link" href="#main-content">
          Skip to content
        </a>
        <header className="site-header">
          <Link className="brand" href="/">
            <span className="brand-mark" aria-hidden="true">
              KQ
            </span>
            <span>KubeQueue</span>
          </Link>
          <nav aria-label="Primary navigation">
            <Link href="/">Jobs</Link>
            <Link href="/queue">Queue</Link>
            <Link href="/jobs/new">Submit</Link>
            <Link href="/projects">Projects</Link>
            <Link href="/access">Access</Link>
            {canReadAudit ? <Link href="/audit">Audit</Link> : null}
            {canReadSupport ? <Link href="/support">Support</Link> : null}
            <Link href="/settings">Settings</Link>
            <Link href={session ? '/logout' : '/login'}>{session ? 'Sign out' : 'Sign in'}</Link>
          </nav>
          <SystemIndicator />
        </header>
        <div id="main-content" tabIndex={-1}>
          {children}
        </div>
      </body>
    </html>
  );
}
