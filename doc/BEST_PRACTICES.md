# Best Practices

## Network Security

### When adapter and backend are on different infrastructure

If your adapter and backend service run on **separate hosts/VPCs**, restrict access to port `9090` using host firewall rules.

#### Using `ufw` (Ubuntu/Debian)
```bash
# Allow only your backend service IP (replace with actual IP)
sudo ufw allow from 203.0.113.45 to any port 9090

# Deny all other access to port 9090
sudo ufw deny 9090

# Verify the rules
sudo ufw status
```

#### Using `iptables` (generic Linux)
```bash
# Allow specific backend IP
sudo iptables -A INPUT -p tcp --dport 9090 -s 203.0.113.45 -j ACCEPT

# Deny all other traffic to port 9090
sudo iptables -A INPUT -p tcp --dport 9090 -j DROP

# Save rules (persistent)
sudo iptables-save | sudo tee /etc/iptables/rules.v4
```

#### Using cloud security groups (AWS/GCP/Azure)
- Create inbound rule: `TCP port 9090` from your backend service's security group/IP only
- Deny all other traffic

*Part of the spark-prime infra observability platform.*