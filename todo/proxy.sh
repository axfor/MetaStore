echo "export https_proxy=http://127.0.0.1:7890" >> ~/.bash_profile
echo "export http_proxy=http://127.0.0.1:7890" >> ~/.bash_profile
echo "export all_proxy=socks5://127.0.0.1:7890" >> ~/.bash_profile

echo "export https_proxy=http://127.0.0.1:7890" >> ~/.zshrc
echo "export http_proxy=http://127.0.0.1:7890" >> ~/.zshrc
echo "export all_proxy=socks5://127.0.0.1:7890" >> ~/.zshrc

source ~/.bash_profile  
source ~/.zshrc
