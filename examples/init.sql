-- Example database initialization file for feature branch deployments
-- This file runs when the database container starts for the first time

-- Create extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Create tables (if your app doesn't handle migrations)
-- Usually your app handles this, so you might only need seed data

-- Example: Users table
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    email VARCHAR(255) UNIQUE NOT NULL,
    name VARCHAR(255),
    password_hash VARCHAR(255),
    role VARCHAR(50) DEFAULT 'user',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Example: Test users for staging
INSERT INTO users (email, name, password_hash, role) VALUES
    ('admin@example.com', 'Admin User', crypt('password123', gen_salt('bf')), 'admin'),
    ('user@example.com', 'Test User', crypt('password123', gen_salt('bf')), 'user'),
    ('qa@example.com', 'QA Tester', crypt('password123', gen_salt('bf')), 'user')
ON CONFLICT (email) DO NOTHING;

-- Example: Products table with test data
CREATE TABLE IF NOT EXISTS products (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    price DECIMAL(10, 2),
    stock INTEGER DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

INSERT INTO products (name, description, price, stock) VALUES
    ('Test Product 1', 'A sample product for testing', 29.99, 100),
    ('Test Product 2', 'Another sample product', 49.99, 50),
    ('Premium Item', 'A premium test item', 199.99, 10)
ON CONFLICT DO NOTHING;

-- Example: Orders table
CREATE TABLE IF NOT EXISTS orders (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID REFERENCES users(id),
    status VARCHAR(50) DEFAULT 'pending',
    total DECIMAL(10, 2),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Add some test orders
INSERT INTO orders (user_id, status, total)
SELECT 
    u.id,
    'completed',
    99.99
FROM users u WHERE u.email = 'user@example.com'
ON CONFLICT DO NOTHING;

-- Grant permissions (if needed)
-- GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO app;
-- GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO app;

-- Output confirmation
DO $$
BEGIN
    RAISE NOTICE 'Database initialization complete!';
    RAISE NOTICE 'Test accounts: admin@example.com, user@example.com, qa@example.com';
    RAISE NOTICE 'Password for all: password123';
END
$$;
