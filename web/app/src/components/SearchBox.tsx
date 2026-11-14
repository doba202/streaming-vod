import { useEffect, useRef, useState, type FormEvent } from 'react';

export interface SearchBoxProps {
  initial?: string;
  delay?: number; // ms chờ sau lần gõ cuối rồi mới tìm (debounce)
  onSearch: (q: string) => void;
}

// SearchBox gọi onSearch với từ khoá đã trim, sau khi người dùng ngừng gõ `delay` ms, hoặc ngay khi
// nhấn Enter. Không gọi lại nếu từ khoá không đổi.
export function SearchBox({ initial = '', delay = 300, onSearch }: SearchBoxProps) {
  const [value, setValue] = useState(initial);
  const lastEmitted = useRef(initial.trim());
  const onSearchRef = useRef(onSearch);
  onSearchRef.current = onSearch;

  const emit = (raw: string) => {
    const q = raw.trim();
    if (q === lastEmitted.current) return;
    lastEmitted.current = q;
    onSearchRef.current(q);
  };

  useEffect(() => {
    const t = setTimeout(() => emit(value), delay);
    return () => clearTimeout(t);
  }, [value, delay]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    emit(value);
  };

  return (
    <form role="search" className="search-box" onSubmit={submit}>
      <input
        type="search"
        aria-label="Tìm phim"
        placeholder="Tìm phim (gõ có dấu hoặc không dấu)…"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        autoFocus
      />
    </form>
  );
}
