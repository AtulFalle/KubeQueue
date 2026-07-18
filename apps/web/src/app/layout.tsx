import type { Metadata } from 'next';
import Link from 'next/link';
import type { ReactNode } from 'react';

import './styles.css';

export const metadata: Metadata = {
  title: 'KubeQueue',
  description: 'Lightweight control and visibility for Kubernetes batch jobs.',
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <a className="skip-link" href="#main-content">Skip to content</a>
        <header className="site-header">
          <Link className="brand" href="/">
            <span className="brand-mark" aria-hidden="true">KQ</span>
            <span>KubeQueue</span>
          </Link>
          <nav aria-label="Primary navigation">
            <Link href="/">Jobs</Link>
            <Link href="/queue">Queue</Link>
            <Link href="/jobs/new">Submit</Link>
          </nav>
          <span className="cluster-indicator">Live inventory</span>
        </header>
        <div id="main-content" tabIndex={-1}>{children}</div>
      </body>
    </html>
  );
}
