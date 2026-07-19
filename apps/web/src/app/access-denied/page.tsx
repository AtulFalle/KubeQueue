import Link from 'next/link';

export default function AccessDeniedPage() {
  return (
    <main className="page-shell narrow">
      <section className="panel">
        <h1>Access denied</h1>
        <p>Your identity was verified, but it is not authorized for this installation.</p>
        <Link className="button primary" href="/login">
          Try another account
        </Link>
      </section>
    </main>
  );
}
