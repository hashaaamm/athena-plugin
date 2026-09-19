import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Package, Plus, Trash } from "@phosphor-icons/react";
import { toast } from "sonner";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PageSpinner } from "@/components/ui/spinner";
import { ApiError } from "@/lib/api/client";
import { useCreateItem, useDeleteItem, useItems, type Item } from "@/lib/api/items";
import { formatRelativeDay } from "@/lib/format";

/**
 * The example resource, end to end: list, create, delete.
 *
 * It is here to be read and then deleted. What is worth copying is the shape — the validation
 * schema next to the form, every state of the request rendered, and the server as the only
 * source of truth about what exists.
 */

// Mirrors the backend's own constraints (app/schemas/item.py). Duplicated deliberately: the
// server MUST validate regardless, and the client validates so the user is told before a round
// trip. If they disagree, the server wins and the form shows its error.
const schema = z.object({
  name: z.string().min(1, "A name is required").max(200, "200 characters at most"),
  description: z.string().max(2000, "2000 characters at most").optional(),
});

type Values = z.infer<typeof schema>;

const EMPTY: Values = { name: "", description: "" };

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.message : fallback;
}

function CreateItemForm() {
  const create = useCreateItem();
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: EMPTY,
  });

  const submit = handleSubmit(async (values) => {
    try {
      await create.mutateAsync({
        name: values.name,
        // An empty input is "no description", not an empty description.
        description: values.description?.trim() ? values.description : null,
      });
      reset(EMPTY);
      toast.success(`Created ${values.name}`);
    } catch (err) {
      toast.error(errorMessage(err, "Could not create the item"));
    }
  });

  return (
    <form onSubmit={submit} className="rounded-2xl border border-hairline bg-surface p-[22px]">
      <div className="flex flex-col gap-3 sm:flex-row">
        <div className="flex-1">
          <label htmlFor="name" className="mb-1 block text-[12.5px] font-medium text-body">
            Name
          </label>
          <Input id="name" placeholder="widget" aria-invalid={Boolean(errors.name)} {...register("name")} />
          {errors.name && (
            <p role="alert" className="mt-1 text-[12px] text-destructive">
              {errors.name.message}
            </p>
          )}
        </div>
        <div className="flex-[2]">
          <label htmlFor="description" className="mb-1 block text-[12.5px] font-medium text-body">
            Description <span className="text-faint">(optional)</span>
          </label>
          <Input id="description" placeholder="What it is for" {...register("description")} />
          {errors.description && (
            <p role="alert" className="mt-1 text-[12px] text-destructive">
              {errors.description.message}
            </p>
          )}
        </div>
        <div className="flex items-end">
          <Button type="submit" disabled={isSubmitting}>
            <Plus weight="bold" />
            Add item
          </Button>
        </div>
      </div>
    </form>
  );
}

function ItemRow({ item }: { item: Item }) {
  const remove = useDeleteItem();
  // Two clicks rather than a modal: the same protection against a mis-click, without a dialog
  // to mount, trap focus in and close.
  const [confirming, setConfirming] = useState(false);

  async function onDelete() {
    if (!confirming) {
      setConfirming(true);
      return;
    }
    try {
      await remove.mutateAsync(item.id);
      toast.success(`Deleted ${item.name}`);
    } catch (err) {
      setConfirming(false);
      toast.error(errorMessage(err, "Could not delete the item"));
    }
  }

  return (
    <tr className="border-t border-hairline">
      <td className="px-5 py-3 text-[13.5px] font-medium text-ink">{item.name}</td>
      <td className="px-5 py-3 text-[13px] text-subtext">{item.description ?? "—"}</td>
      <td className="px-5 py-3 text-[13px] text-mute">{formatRelativeDay(item.created_at)}</td>
      <td className="px-5 py-3 text-right">
        <Button
          variant={confirming ? "destructive" : "ghost"}
          size="sm"
          onClick={onDelete}
          disabled={remove.isPending}
          aria-label={confirming ? `Confirm deleting ${item.name}` : `Delete ${item.name}`}
        >
          <Trash />
          {confirming ? "Confirm" : "Delete"}
        </Button>
      </td>
    </tr>
  );
}

function EmptyState() {
  return (
    <div className="rounded-2xl border border-dashed border-hairline bg-surface p-12 text-center">
      <Package size={26} className="mx-auto text-faint" />
      <p className="mt-3 text-[13.5px] font-medium text-ink">No items yet</p>
      <p className="mt-1 text-[13px] text-subtext">Add one above, or run `just db-seed`.</p>
    </div>
  );
}

export function ItemsPage() {
  const items = useItems();

  return (
    <div className="mx-auto max-w-[1080px] px-10 pb-[60px] pt-8">
      <h1 className="text-[26px] font-bold tracking-[-0.03em] text-ink">Items</h1>
      <p className="mt-1 text-[13.5px] text-subtext">
        The backend&rsquo;s example resource, through the generated client.
      </p>

      <div className="mt-6">
        <CreateItemForm />
      </div>

      {/* Every state the request has is rendered. A page that only draws the happy path shows an
          empty table for "loading", "empty" and "the API is down" alike. */}
      {items.isPending && <PageSpinner />}

      {items.isError && (
        <div
          role="alert"
          className="mt-6 rounded-2xl border border-hairline bg-danger-soft p-5 text-[13.5px] text-destructive"
        >
          {errorMessage(items.error, "Could not load items")}
        </div>
      )}

      {items.data && items.data.count === 0 && (
        <div className="mt-6">
          <EmptyState />
        </div>
      )}

      {items.data && items.data.count > 0 && (
        <div className="mt-6 overflow-hidden rounded-2xl border border-hairline bg-surface">
          <table className="w-full text-left">
            <caption className="sr-only">Items</caption>
            <thead>
              <tr className="text-[11px] font-semibold uppercase tracking-[0.05em] text-faint">
                <th scope="col" className="px-5 py-3">
                  Name
                </th>
                <th scope="col" className="px-5 py-3">
                  Description
                </th>
                <th scope="col" className="px-5 py-3">
                  Created
                </th>
                <th scope="col" className="px-5 py-3 text-right">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.data.items.map((item) => (
                <ItemRow key={item.id} item={item} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
