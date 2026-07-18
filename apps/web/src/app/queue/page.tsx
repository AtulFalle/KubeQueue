import { redirect } from 'next/navigation';

export default function QueuePage() {
  redirect('/?status=QUEUED');
}
