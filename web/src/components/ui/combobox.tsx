// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Check, ChevronsUpDown } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

/**
 * Combobox — the FILTERED sibling of `Select` (W3C ARIA APG `combobox` pattern:
 * an editable input over a filtered listbox popup). The trigger reads like an Input
 * (h-8, hairline, copper focus ring, ChevronsUpDown affordance) and opens a Popover
 * holding a `cmdk` Command: a type-to-filter input, a listbox of options, and an
 * empty state. All the combobox a11y roles come from cmdk — the input is
 * `role="combobox"` with `aria-autocomplete="list"`, `aria-expanded`,
 * `aria-controls` and a moving `aria-activedescendant`; the list is
 * `role="listbox"`; each row is `role="option"` with `aria-selected`. Per APG, DOM
 * focus stays on the input while Arrow keys move the active descendant; Enter selects
 * and Escape closes, both returning focus to the trigger. Use for filtered selectors
 * (tenant, model, resource) where `Select` would be too long to scan.
 */

export interface ComboboxOption {
  /** Stable value returned by `onChange` and used to mark the chosen row. */
  value: string
  /** Human label shown in the row and on the trigger when chosen. */
  label: string
  /** Extra terms the filter should match (synonyms, ids, aliases). */
  keywords?: string[]
  /** Render the row non-selectable. */
  disabled?: boolean
}

export interface ComboboxProps {
  options: ComboboxOption[]
  value: string | null
  onChange: (value: string | null) => void
  /** Trigger text when nothing is selected. */
  placeholder?: string
  /** Filter input placeholder. */
  searchPlaceholder?: string
  /** Shown inside the listbox when the filter matches nothing. */
  emptyText?: string
  disabled?: boolean
  /** Allow deselecting by re-choosing the selected row. */
  clearable?: boolean
  /** Id forwarded to the trigger so a `<label htmlFor>` can associate. */
  id?: string
  /** Accessible name for the trigger. A `<label htmlFor>` does NOT name a button,
   * so wrap in `Field` (which threads `aria-labelledby`) or pass one of these. */
  'aria-label'?: string
  'aria-labelledby'?: string
  'aria-describedby'?: string
  'aria-invalid'?: boolean
  className?: string
}

export function Combobox({
  options,
  value,
  onChange,
  placeholder = 'Select…',
  searchPlaceholder = 'Search…',
  emptyText = 'No results.',
  disabled = false,
  clearable = false,
  id,
  'aria-label': ariaLabel,
  'aria-labelledby': ariaLabelledby,
  'aria-describedby': ariaDescribedby,
  'aria-invalid': ariaInvalid,
  className,
}: ComboboxProps) {
  const { t } = useTranslation('common')
  const [open, setOpen] = useState(false)

  const selected =
    value == null ? undefined : options.find((o) => o.value === value)

  const choose = (next: string) => {
    // Re-choosing the current value clears it when allowed; otherwise it is a no-op
    // confirm of the existing selection.
    if (clearable && next === value) {
      onChange(null)
    } else {
      onChange(next)
    }
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          id={id}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-label={ariaLabel}
          aria-labelledby={ariaLabel ? undefined : ariaLabelledby}
          aria-describedby={ariaDescribedby}
          aria-invalid={ariaInvalid}
          disabled={disabled}
          data-slot="combobox-trigger"
          className={cn(
            'flex h-8 w-full items-center justify-between gap-2 rounded-md border border-border-strong bg-surface px-2.5',
            'text-sm text-foreground transition-colors outline-none',
            'focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background',
            'aria-[invalid=true]:border-danger aria-[invalid=true]:ring-danger',
            'disabled:pointer-events-none disabled:opacity-50 disabled:bg-muted',
            '[&_svg]:size-4 [&_svg]:shrink-0',
            className,
          )}
        >
          <span
            className={cn(
              'line-clamp-1 text-left',
              !selected && 'text-muted-foreground',
            )}
          >
            {selected ? selected.label : placeholder}
          </span>
          <ChevronsUpDown
            className="size-4 text-muted-foreground"
            aria-hidden="true"
          />
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        sideOffset={4}
        className="w-[var(--radix-popover-trigger-width)] min-w-[12rem] p-0"
      >
        <Command label={searchPlaceholder}>
          <CommandInput placeholder={searchPlaceholder} />
          <CommandList label={t('commandPalette.results')}>
            <CommandEmpty>{emptyText}</CommandEmpty>
            {options.map((option) => (
              <CommandItem
                key={option.value}
                value={option.value}
                keywords={[option.label, ...(option.keywords ?? [])]}
                disabled={option.disabled}
                onSelect={choose}
              >
                <span className="line-clamp-1 flex-1">{option.label}</span>
                <Check
                  className={cn(
                    'ml-auto size-4',
                    option.value === value ? 'opacity-100' : 'opacity-0',
                  )}
                  aria-hidden="true"
                />
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
