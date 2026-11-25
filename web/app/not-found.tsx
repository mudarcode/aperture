import Link from "next/link";
import { EmptyState } from "@/components/ui/states";

export default function NotFound() {
  return (
    <EmptyState title="Not found">
      <Link className="text-accent underline" href="/services">
        Back to services
      </Link>
    </EmptyState>
  );
}
