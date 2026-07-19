import { assertBFFConfiguration } from './lib/bff-config';

export async function register() {
  if (process.env.NEXT_RUNTIME === 'nodejs') {
    assertBFFConfiguration();
  }
}
