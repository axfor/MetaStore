#!/bin/bash

if [[ "$OSTYPE" == "darwin"* ]]; then
  sed -i '' '/export https_proxy=/d' ~/.bash_profile
  sed -i '' '/export http_proxy=/d' ~/.bash_profile
  sed -i '' '/export all_proxy=/d' ~/.bash_profile
  sed -i '' '/export https_proxy=/d' ~/.zshrc
  sed -i '' '/export http_proxy=/d' ~/.zshrc
  sed -i '' '/export all_proxy=/d' ~/.zshrc 
else
  sed -i '/export https_proxy=/d' ~/.bash_profile
  sed -i '/export http_proxy=/d' ~/.bash_profile
  sed -i '/export all_proxy=/d' ~/.bash_profile
  sed -i '/export https_proxy=/d' ~/.zshrc
  sed -i '/export http_proxy=/d' ~/.zshrc
  sed -i '/export all_proxy=/d' ~/.zshrc
fi


echo "export https_proxy=http://127.0.0.1:7890" >> ~/.bash_profile
echo "export http_proxy=http://127.0.0.1:7890" >> ~/.bash_profile
echo "export all_proxy=socks5://127.0.0.1:7890" >> ~/.bash_profile

echo "export https_proxy=http://127.0.0.1:7890" >> ~/.zshrc
echo "export http_proxy=http://127.0.0.1:7890" >> ~/.zshrc
echo "export all_proxy=socks5://127.0.0.1:7890" >> ~/.zshrc  

 

if [[ "$OSTYPE" == "darwin"* ]]; then
  sed -i '' '/export ANTHROPIC_BASE_URL=/d' ~/.bash_profile
  sed -i '' '/export ANTHROPIC_AUTH_TOKEN=/d' ~/.bash_profile
  sed -i '' '/export ANTHROPIC_BASE_URL=/d' ~/.zshrc
  sed -i '' '/export ANTHROPIC_AUTH_TOKEN=/d' ~/.zshrc
else
  sed -i '/export ANTHROPIC_BASE_URL=/d' ~/.bash_profile
  sed -i '/export ANTHROPIC_AUTH_TOKEN=/d' ~/.bash_profile
  sed -i '/export ANTHROPIC_BASE_URL=/d' ~/.zshrc
  sed -i '/export ANTHROPIC_AUTH_TOKEN=/d' ~/.zshrc
fi

echo 'export ANTHROPIC_BASE_URL="https://code.newcli.com/claude"' >> ~/.bash_profile  
echo 'export ANTHROPIC_AUTH_TOKEN=""' >> ~/.bash_profile  

echo 'export ANTHROPIC_BASE_URL="https://code.newcli.com/claude"' >> ~/.zshrc  
echo 'export ANTHROPIC_AUTH_TOKEN=""' >> ~/.zshrc  



source ~/.bash_profile  && source ~/.zshrc




cat  ~/.bash_profile  

cat source ~/.zshrc