package pf::Switch::Meraki::AP_http;

=head1 NAME

pf::Switch::Meraki::AP_http

=head1 SYNOPSIS

The pf::Switch::Meraki::AP_http module implements an object oriented interface to
manage the external captive portal on Meraki access points

=head1 STATUS

Developed and tested on a MR12 access point

=head1 BUGS AND LIMITATIONS

=head2 Cannot reevaluate the access

There is currently no way to reevaluate the access of the device.
There is neither an API access or a RADIUS disconnect that can be sent either to the AP or to the controller.

=head2 client IP cannot be computed from parseUrl

The Meraki sends a NATed IP address in the URL even though the client is bridged.
The only workaround is to have the DHCP traffic forwarded to PacketFence.

=cut

use strict;
use warnings;

use base ('pf::Switch');

use pf::constants;
use pf::config;
use pf::util;
use pf::node;

=head1 SUBROUTINES

=cut

# CAPABILITIES
# access technology supported
sub description { 'Meraki cloud controller' }
sub supportsWirelessMacAuth { return $TRUE; }
sub supportsExternalPortal { return $TRUE; }
sub supportsWebFormRegistration { return $TRUE }

=head2 getVersion - obtain image version information from switch

=cut

sub getVersion {
    my ($self) = @_;
    my $logger = $self->logger;
    $logger->info("we don't know how to determine the version through SNMP !");
    return '1';
}

=head2 parseUrl

This is called when we receive a http request from the device and return specific attributes:

client mac address
SSID
client ip address
redirect url
grant url
status code

=cut

sub parseUrl {
    my($self, $req, $r) = @_;
    my $logger = $self->logger;
    my $connection = $r->connection;
    $self->synchronize_locationlog("0", "0", clean_mac($$req->param('client_mac')),
        0, $WIRELESS_MAC_AUTH, "", clean_mac($$req->param('client_mac')), "Unknown"
    );

    return (clean_mac($$req->param('client_mac')),"Unknown",$$req->param('client_ip'),$$req->param('continue_url'),undef,"200");
}

sub parseSwitchIdFromRequest {
    my($class, $req) = @_;
    return $$req->param('ap_mac');
}

=head2 returnRadiusAccessAccept

Prepares the RADIUS Access-Accept reponse for the network device.

Overriding the default implementation for the external captive portal

=cut

sub returnRadiusAccessAccept {
    my ($self, $args) = @_;
    my $logger = $self->logger;

    my $radius_reply_ref = {};
    my $status;

    # should this node be kicked out?
    my $kick = $self->handleRadiusDeny($args);
    return $kick if (defined($kick));

    my $node = $args->{'node_info'};
    my $filter = pf::access_filter::radius->new;
    my $rule = $filter->test('returnRadiusAccessAccept', $args);

    my $violation = pf::violation::violation_view_top($args->{'mac'});
    # if user is unregistered or is in violation then we reject him to show him the captive portal 
    if ( $node->{status} eq $pf::node::STATUS_UNREGISTERED || defined($violation) ){
        $logger->info("[$args->{'mac'}] is unregistered. Refusing access to force the eCWP");
        my $radius_reply_ref = {
            'Tunnel-Medium-Type' => $RADIUS::ETHERNET,
            'Tunnel-Type' => $RADIUS::VLAN,
            'Tunnel-Private-Group-ID' => -1,
        };
        ($radius_reply_ref, $status) = $filter->handleAnswerInRule($rule,$args,$radius_reply_ref);
        return [$status, %$radius_reply_ref];

    }
    else{
        $logger->info("Returning ACCEPT");
        ($radius_reply_ref, $status) = $filter->handleAnswerInRule($rule,$args,$radius_reply_ref);
        return [$status, %$radius_reply_ref];
    }

}

sub getAcceptForm {
    my ( $self, $mac , $destination_url, $cgi_session) = @_;
    my $logger = $self->logger;
    $logger->debug("Creating web release form");

    my $login_url = $cgi_session->param("ecwp-original-param-login_url");
    my $html_form = qq[
        <form name="weblogin_form" method="POST" action="$login_url">
            <input type="hidden" name="Submit2" value="Submit">
            <input type="hidden" name="username" value="$mac">
            <input type="hidden" name="password" value="$mac">
            <input type="hidden" name="success_url" value="$destination_url">
        </form>
        <script language="JavaScript" type="text/javascript">
        window.setTimeout('document.weblogin_form.submit();', 1000);
        </script>
    ];

    $logger->debug("Generated the following html form : ".$html_form);
    return $html_form;
}

=head1 AUTHOR

Inverse inc. <info@inverse.ca>

=head1 COPYRIGHT

Copyright (C) 2005-2016 Inverse inc.

=head1 LICENSE

This program is free software; you can redistribute it and/or
modify it under the terms of the GNU General Public License
as published by the Free Software Foundation; either version 2
of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program; if not, write to the Free Software
Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA  02110-1301,
USA.

=cut

1;

# vim: set shiftwidth=4:
# vim: set expandtab:
# vim: set backspace=indent,eol,start:
