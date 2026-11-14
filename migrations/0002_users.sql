-- M3: tài khoản. Email lưu dạng chữ thường (Go chuẩn hoá trước khi ghi) nên UNIQUE trên email là đủ.
CREATE TABLE users (
  id            uuid PRIMARY KEY,
  email         text NOT NULL UNIQUE CHECK (email = lower(email)),
  password_hash text NOT NULL,
  role          text NOT NULL CHECK (role IN ('admin','viewer')),
  created_at    timestamptz NOT NULL DEFAULT now()
);
