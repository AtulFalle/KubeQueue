const required = [
  'KUBEQUEUE_API_INTERNAL_URL',
  'KUBEQUEUE_PUBLIC_URL',
  'KUBEQUEUE_BFF_INTERNAL_KEY',
] as const;

export type BFFConfig = {
  apiOrigin: string;
  publicOrigin: string;
  internalKey: string;
};

export function assertBFFConfiguration(): void {
  const present = required.filter((name) => Boolean(process.env[name]?.trim()));
  if (present.length === 0) return;
  getBFFConfig();
}

export function getBFFConfig(): BFFConfig {
  const missing = required.filter((name) => !process.env[name]?.trim());
  if (missing.length > 0) {
    throw new Error(`Incomplete browser authentication configuration: ${missing.join(', ')}`);
  }
  const publicOrigin = normalizedOrigin(value('KUBEQUEUE_PUBLIC_URL'), true);
  const internalKey = value('KUBEQUEUE_BFF_INTERNAL_KEY');
  if (internalKey.length < 32) {
    throw new Error('KUBEQUEUE_BFF_INTERNAL_KEY must contain at least 32 characters');
  }
  return {
    apiOrigin: normalizedOrigin(value('KUBEQUEUE_API_INTERNAL_URL'), false),
    publicOrigin,
    internalKey,
  };
}

function value(name: (typeof required)[number]): string {
  const configured = process.env[name]?.trim();
  if (!configured) {
    throw new Error(`Missing browser authentication configuration: ${name}`);
  }
  return configured;
}

function normalizedOrigin(input: string, requireSecure: boolean): string {
  const url = new URL(input);
  if (url.username || url.password || (url.pathname !== '' && url.pathname !== '/')) {
    throw new Error(`${input} must be an origin without credentials or a path`);
  }
  const loopback =
    url.hostname === 'localhost' || url.hostname === '127.0.0.1' || url.hostname === '[::1]';
  if (requireSecure && url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) {
    throw new Error(`${input} must use HTTPS except on loopback`);
  }
  return url.origin;
}
